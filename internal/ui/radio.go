package ui

import (
	"log"

	"github.com/MattiaPun/SubTUI/v2/internal/api"
	tea "github.com/charmbracelet/bubbletea"
)

const defaultRadioSongCount = 20

// Sources are tried in order until one returns new songs
const (
	radioSourceServer = iota
	radioSourceLocal
	radioSourceRandom
	radioSourceNone
)

type radioSeed struct {
	id   string
	kind int       // displaySongs, displayAlbums or displayArtist
	song *api.Song // Set for song seeds
}

func radioSongCount() int {
	if count := api.AppConfig.Radio.SongCount; count > 0 {
		return count
	}

	return defaultRadioSongCount
}

// startRadio adds songs similar to the selected song, album or artist to the queue.
// Outside of the main view, or when it is empty, the currently playing song is used as the seed.
func startRadio(m model) (model, tea.Cmd) {
	if m.radioLoading || m.focus == focusSearch {
		return m, nil
	}

	var seed radioSeed

	if m.focus == focusMain && cursorInBounds(m) {
		switch {
		case m.viewMode == viewQueue:
			seed = songSeed(m.queue[m.cursorMain])
		case m.displayMode == displaySongs:
			seed = songSeed(m.songs[m.cursorMain])
		case m.displayMode == displayAlbums:
			seed = radioSeed{id: m.albums[m.cursorMain].ID, kind: displayAlbums}
		case m.displayMode == displayArtist:
			seed = radioSeed{id: m.artists[m.cursorMain].ID, kind: displayArtist}
		}
	} else if len(m.queue) > 0 {
		seed = songSeed(m.queue[m.queueIndex])
	}

	if seed.id == "" {
		return m, nil
	}

	return m.requestRadio(m.nextRadioSource(-1), seed)
}

func songSeed(song api.Song) radioSeed {
	return radioSeed{id: song.ID, kind: displaySongs, song: &song}
}

// refillRadio tops up the queue with similar songs when it is about to run out
func (m *model) refillRadio() tea.Cmd {
	cfg := api.AppConfig.Radio
	if !cfg.AutoRefill || m.radioLoading || m.loopMode != LoopNone || len(m.queue) == 0 {
		return nil
	}

	if len(m.queue)-1-m.queueIndex > cfg.RefillThreshold {
		return nil
	}

	updated, cmd := m.requestRadio(m.nextRadioSource(-1), songSeed(m.queue[m.queueIndex]))
	*m = updated
	return cmd
}

// nextRadioSource returns the first enabled source after the given one
func (m model) nextRadioSource(after int) int {
	cfg := api.AppConfig.Radio

	for source := after + 1; source < radioSourceNone; source++ {
		switch source {
		case radioSourceServer:
			if cfg.ServerSimilarity && !m.serverSimilarityDown {
				return source
			}
		case radioSourceLocal:
			if cfg.LocalMix {
				return source
			}
		case radioSourceRandom:
			if cfg.RandomFallback {
				return source
			}
		}
	}

	return radioSourceNone
}

func (m model) requestRadio(source int, seed radioSeed) (model, tea.Cmd) {
	if source == radioSourceNone {
		return m, nil
	}

	queued := make(map[string]bool, len(m.queue))
	for _, song := range m.queue {
		queued[song.ID] = true
	}

	// Snapshot what the filters need, the command runs outside the update loop
	filters := api.AppConfig.Filters
	filterModel := model{}
	if filters.ExcludeFavorites {
		filterModel.starredMap = make(map[string]bool, len(m.starredMap))
		for id, starred := range m.starredMap {
			filterModel.starredMap[id] = starred
		}
	}

	skip := func(song api.Song) bool {
		return queued[song.ID] || isSongExcluded(filterModel, song, filters)
	}

	m.radioLoading = true
	return m, radioCmd(source, seed, skip)
}

func (m model) handleRadio(msg radioResultMsg) (tea.Model, tea.Cmd) {
	m.radioLoading = false

	if msg.err != nil {
		log.Printf("[Radio] Failed to fetch songs from source %d", msg.source) // Error text contains the request URL with credentials
		if msg.source == radioSourceServer {
			m.serverSimilarityDown = true // Don't wait on a broken provider again
		}
		msg.songs = nil
	}

	songs := m.newRadioSongs(msg.songs, msg.seed.song)

	if len(songs) == 0 {
		if next := m.nextRadioSource(msg.source); next != radioSourceNone {
			log.Printf("[Radio] No songs from source %d, trying source %d", msg.source, next)
			return m.requestRadio(next, msg.seed)
		}
	}

	if len(m.queue) == 0 { // Start a new queue with the seed song first
		if msg.seed.song != nil {
			songs = append([]api.Song{*msg.seed.song}, songs...)
		}

		if len(songs) == 0 {
			log.Printf("[Radio] No songs found")
			return m, nil
		}

		m.queue = songs
		return m, m.playQueueIndex(0, false)
	}

	if len(songs) == 0 {
		log.Printf("[Radio] No new songs found")
		return m, nil
	}

	m.queue = append(m.queue, songs...)
	m.syncNextSong()

	return m, m.savePlayQueue()
}

// newRadioSongs drops the seed, songs already in the queue, duplicates and excluded songs
func (m model) newRadioSongs(songs []api.Song, seed *api.Song) []api.Song {
	seen := make(map[string]bool, len(m.queue)+1)
	for _, song := range m.queue {
		seen[song.ID] = true
	}
	if seed != nil {
		seen[seed.ID] = true
	}

	filters := api.AppConfig.Filters
	var result []api.Song
	for _, song := range songs {
		if seen[song.ID] || isSongExcluded(m, song, filters) {
			continue
		}

		seen[song.ID] = true
		result = append(result, song)
	}

	return result
}
