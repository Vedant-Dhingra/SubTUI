package ui

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/MattiaPun/SubTUI/v2/internal/api"
	tea "github.com/charmbracelet/bubbletea"
)

func songs(ids ...string) []api.Song {
	var result []api.Song
	for _, id := range ids {
		result = append(result, api.Song{ID: id, Title: id})
	}
	return result
}

func songIDs(songs []api.Song) []string {
	var ids []string
	for _, song := range songs {
		ids = append(ids, song.ID)
	}
	return ids
}

func setRadioConfig(t *testing.T, radio api.Radio) {
	prev := api.AppConfig
	t.Cleanup(func() { api.AppConfig = prev })
	api.AppConfig.Radio = radio
	api.AppConfig.Filters = api.Filters{}
}

func assertIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}

var allSources = api.Radio{ServerSimilarity: true, LocalMix: true, RandomFallback: true}

func TestNewRadioSongsSkipsSeedQueuedAndDuplicates(t *testing.T) {
	setRadioConfig(t, api.Radio{})
	m := model{queue: songs("a", "b")}
	seed := api.Song{ID: "s"}

	got := m.newRadioSongs(songs("s", "a", "c", "c", "d"), &seed)

	assertIDs(t, songIDs(got), "c", "d")
}

func TestHandleRadioAppendsToExistingQueue(t *testing.T) {
	setRadioConfig(t, api.Radio{})
	m := model{queue: songs("a", "b"), queueIndex: 1, radioLoading: true}

	res, _ := m.handleRadio(radioResultMsg{songs: songs("b", "c"), seed: songSeed(m.queue[1])})
	m = res.(model)

	assertIDs(t, songIDs(m.queue), "a", "b", "c")
	if m.queueIndex != 1 {
		t.Errorf("queueIndex = %d, want 1", m.queueIndex)
	}
	if m.radioLoading {
		t.Error("radioLoading still set")
	}
}

func TestHandleRadioStartsEmptyQueueWithSeed(t *testing.T) {
	setRadioConfig(t, api.Radio{})

	res, cmd := model{}.handleRadio(radioResultMsg{songs: songs("c", "d"), seed: songSeed(api.Song{ID: "s"})})

	assertIDs(t, songIDs(res.(model).queue), "s", "c", "d")
	if cmd == nil {
		t.Error("expected playback command")
	}
}

func TestNextRadioSource(t *testing.T) {
	setRadioConfig(t, allSources)
	m := model{}

	if got := m.nextRadioSource(-1); got != radioSourceServer {
		t.Errorf("first source = %d, want server", got)
	}

	m.serverSimilarityDown = true
	if got := m.nextRadioSource(-1); got != radioSourceLocal {
		t.Errorf("first source with server down = %d, want local", got)
	}

	api.AppConfig.Radio.LocalMix = false
	if got := m.nextRadioSource(radioSourceServer); got != radioSourceRandom {
		t.Errorf("after server = %d, want random", got)
	}
	if got := m.nextRadioSource(radioSourceRandom); got != radioSourceNone {
		t.Errorf("after random = %d, want none", got)
	}
}

func TestHandleRadioOnlySeedReturnedTriesNextSource(t *testing.T) {
	setRadioConfig(t, allSources)
	m := model{queue: songs("s")}

	res, cmd := m.handleRadio(radioResultMsg{songs: songs("s"), seed: songSeed(m.queue[0]), source: radioSourceServer})
	m = res.(model)

	assertIDs(t, songIDs(m.queue), "s")
	if cmd == nil || !m.radioLoading {
		t.Fatal("expected a request to the next source")
	}
	if m.serverSimilarityDown {
		t.Error("empty result should not disable the server provider")
	}
}

func TestHandleRadioServerErrorDisablesProvider(t *testing.T) {
	setRadioConfig(t, allSources)
	m := model{queue: songs("a")}

	res, cmd := m.handleRadio(radioResultMsg{err: errors.New("timeout"), source: radioSourceServer})
	m = res.(model)

	if cmd == nil || !m.radioLoading {
		t.Fatal("expected a request to the next source")
	}
	if !m.serverSimilarityDown {
		t.Error("server provider not disabled after error")
	}
}

func TestHandleRadioNothingFoundKeepsQueue(t *testing.T) {
	setRadioConfig(t, allSources)
	m := model{queue: songs("a")}

	res, cmd := m.handleRadio(radioResultMsg{source: radioSourceRandom})
	m = res.(model)

	assertIDs(t, songIDs(m.queue), "a")
	if cmd != nil || m.radioLoading {
		t.Error("expected no further requests")
	}
}

func TestRefillRadio(t *testing.T) {
	setRadioConfig(t, api.Radio{AutoRefill: true, RefillThreshold: 2, LocalMix: true})

	m := model{queue: songs("a", "b", "c", "d"), queueIndex: 0}
	if m.refillRadio() != nil {
		t.Error("refilled with 3 songs left")
	}

	m.queueIndex = 1
	if m.refillRadio() == nil || !m.radioLoading {
		t.Error("expected refill with 2 songs left")
	}
	if m.refillRadio() != nil {
		t.Error("refilled while a request is in flight")
	}

	m = model{queue: songs("a"), loopMode: LoopAll}
	if m.refillRadio() != nil {
		t.Error("refilled while looping")
	}
}

func TestMixProfileScore(t *testing.T) {
	p := newMixProfile()
	p.add("Rock", "artist1", 1994)
	p.add("rock", "artist1", 1996)
	p.add("Grunge", "artist1", 1995)

	tests := []struct {
		name string
		song api.Song
		want float64
	}{
		{"top genre, same era", api.Song{Genre: "ROCK", Year: 1997}, 4 + 2},
		{"other seed genre", api.Song{Genre: "grunge"}, 3},
		{"same artist, far era", api.Song{ArtistID: "artist1", Year: 2020}, 1.5},
		{"no match", api.Song{Genre: "Jazz", Year: 1950, ArtistID: "x"}, 0},
		{"era only", api.Song{Year: 1990}, 1.5},
	}

	for _, tt := range tests {
		if got := p.score(tt.song); got != tt.want {
			t.Errorf("%s: score = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPickMixFiltersAndLimitsArtists(t *testing.T) {
	p := newMixProfile()
	p.add("Rock", "", 2000)

	var candidates []api.Song
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5"} { // One prolific artist
		candidates = append(candidates, api.Song{ID: id, ArtistID: "a", Genre: "Rock", Year: 2000})
	}
	candidates = append(candidates,
		api.Song{ID: "b1", ArtistID: "b", Genre: "Rock"},
		api.Song{ID: "b1", ArtistID: "b", Genre: "Rock"},             // Duplicate
		api.Song{ID: "c1", ArtistID: "c", Genre: "Jazz", Year: 1960}, // Unrelated
		api.Song{ID: "d1", ArtistID: "d", Genre: "Rock"},             // Skipped
	)

	skip := func(song api.Song) bool { return song.ID == "d1" }
	mix := pickMix(p, candidates, skip, 8, rand.New(rand.NewSource(1)))

	perArtist := map[string]int{}
	for _, song := range mix {
		perArtist[song.ArtistID]++
	}

	if len(mix) != 3 {
		t.Fatalf("mix = %v, want 3 songs", songIDs(mix))
	}
	if perArtist["a"] != 2 || perArtist["b"] != 1 {
		t.Errorf("per artist = %v, want a:2 b:1", perArtist)
	}
}

func pressKeys(m model, keys ...string) model {
	for _, k := range keys {
		res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = res.(model)
	}
	return m
}

func TestStartRadioKey(t *testing.T) {
	setRadioConfig(t, api.Radio{LocalMix: true})
	api.AppConfig.Keybinds = api.Keybinds{Other: api.OtherKeybinds{StartRadio: []string{"r"}}}
	base := model{queue: songs("q1", "q2"), starredMap: map[string]bool{}, selectionMap: map[int]bool{}}

	tests := []struct {
		name  string
		setup func(m *model)
		keys  []string
	}{
		{"empty main view uses current song", func(m *model) { m.focus = focusMain }, []string{"r"}},
		{"selected song", func(m *model) { m.focus = focusMain; m.songs = songs("s1") }, []string{"r"}},
		{"sidebar uses current song", func(m *model) { m.focus = focusSidebar }, []string{"r"}},
		{"after gg", func(m *model) { m.focus = focusMain; m.songs = songs("s1") }, []string{"g", "g", "r"}},
	}

	for _, tt := range tests {
		m := base
		tt.setup(&m)
		if m = pressKeys(m, tt.keys...); !m.radioLoading {
			t.Errorf("%s: radio not started", tt.name)
		}
	}
}

func TestGoComboDoesNotStick(t *testing.T) {
	m := pressKeys(model{focus: focusMain}, "g", "g")
	if m.lastKey != "" {
		t.Errorf("lastKey = %q after gg, want empty", m.lastKey)
	}
}
