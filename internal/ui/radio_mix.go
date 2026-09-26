package ui

import (
	"math/rand"
	"sort"
	"strings"
	"sync"

	"github.com/MattiaPun/SubTUI/v2/internal/api"
)

// Local mix: builds a radio from the library's own tags (genre, year and artist)
// for servers without a similarity provider

const (
	mixYearWindow   = 5   // Years around the seed to look for songs from the same era
	mixMinScore     = 1.5 // Candidates need at least one meaningful match
	mixJitter       = 2.0 // Randomness added to scores so every mix is different
	mixArtistAlbums = 3   // Albums of the seed artist to pull songs from
)

type mixProfile struct {
	genres    map[string]int // Lowercased genre -> occurrences in the seed
	artistIDs map[string]bool
	seedAlbum string
	minYear   int
	maxYear   int
}

func newMixProfile() mixProfile {
	return mixProfile{genres: map[string]int{}, artistIDs: map[string]bool{}}
}

func (p *mixProfile) add(genre string, artistID string, year int) {
	if genre = strings.ToLower(strings.TrimSpace(genre)); genre != "" {
		p.genres[genre]++
	}

	if artistID != "" {
		p.artistIDs[artistID] = true
	}

	if year > 0 {
		if p.minYear == 0 || year < p.minYear {
			p.minYear = year
		}
		if year > p.maxYear {
			p.maxYear = year
		}
	}
}

// topGenres returns up to n genres, most common first
func (p mixProfile) topGenres(n int) []string {
	genres := make([]string, 0, len(p.genres))
	for genre := range p.genres {
		genres = append(genres, genre)
	}

	sort.Slice(genres, func(i, j int) bool {
		if p.genres[genres[i]] != p.genres[genres[j]] {
			return p.genres[genres[i]] > p.genres[genres[j]]
		}
		return genres[i] < genres[j]
	})

	return genres[:min(n, len(genres))]
}

func (p mixProfile) score(song api.Song) float64 {
	score := 0.0

	if genre := strings.ToLower(strings.TrimSpace(song.Genre)); genre != "" && p.genres[genre] > 0 {
		score += 3
		if top := p.topGenres(1); top[0] == genre {
			score++
		}
	}

	if p.minYear > 0 && song.Year > 0 {
		distance := max(p.minYear-song.Year, song.Year-p.maxYear, 0)
		switch {
		case distance <= 2:
			score += 2
		case distance <= mixYearWindow:
			score += 1.5
		case distance <= 2*mixYearWindow:
			score += 0.5
		}
	}

	if p.artistIDs[song.ArtistID] {
		score += 1.5
	}

	return score
}

func buildLocalMix(seed radioSeed, skip func(api.Song) bool, count int) ([]api.Song, error) {
	profile, artistAlbums, seedSongs, err := mixSeed(seed)
	if err != nil {
		return nil, err
	}

	// Never add the seed itself back
	seedIDs := make(map[string]bool, len(seedSongs))
	for _, song := range seedSongs {
		seedIDs[song.ID] = true
	}
	skipSeed := func(song api.Song) bool {
		return seedIDs[song.ID] || skip(song)
	}

	candidates := fetchMixCandidates(profile, artistAlbums, count)
	return pickMix(profile, candidates, skipSeed, count, rand.New(rand.NewSource(rand.Int63()))), nil
}

// mixSeed builds a profile of the seed and returns the seed artist's albums and the seed's own songs
func mixSeed(seed radioSeed) (mixProfile, []api.Album, []api.Song, error) {
	profile := newMixProfile()

	switch seed.kind {
	case displaySongs:
		if seed.song == nil {
			return profile, nil, nil, nil
		}

		song := *seed.song
		profile.add(song.Genre, song.ArtistID, song.Year)
		profile.seedAlbum = song.AlbumID

		var albums []api.Album
		if song.ArtistID != "" {
			albums, _ = api.SubsonicGetArtist(song.ArtistID)
		}
		return profile, albums, []api.Song{song}, nil

	case displayAlbums:
		songs, err := api.SubsonicGetAlbum(seed.id)
		if err != nil {
			return profile, nil, nil, err
		}

		for _, song := range songs {
			profile.add(song.Genre, song.ArtistID, song.Year)
		}
		profile.seedAlbum = seed.id

		var albums []api.Album
		if len(songs) > 0 && songs[0].ArtistID != "" {
			albums, _ = api.SubsonicGetArtist(songs[0].ArtistID)
		}
		return profile, albums, songs, nil

	case displayArtist:
		albums, err := api.SubsonicGetArtist(seed.id)
		if err != nil {
			return profile, nil, nil, err
		}

		profile.artistIDs[seed.id] = true
		for _, album := range albums {
			profile.add(album.Genre, "", album.Year)
		}
		return profile, albums, nil, nil
	}

	return profile, nil, nil, nil
}

// fetchMixCandidates gathers songs sharing the seed's genres, era or artist
func fetchMixCandidates(profile mixProfile, artistAlbums []api.Album, count int) []api.Song {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var candidates []api.Song

	fetch := func(get func() ([]api.Song, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			songs, err := get()
			if err != nil {
				return
			}
			mu.Lock()
			candidates = append(candidates, songs...)
			mu.Unlock()
		}()
	}

	fromYear, toYear := 0, 0
	if profile.minYear > 0 {
		fromYear, toYear = profile.minYear-mixYearWindow, profile.maxYear+mixYearWindow
	}

	genres := profile.topGenres(2)
	for _, genre := range genres {
		if fromYear > 0 { // Same genre and era
			fetch(func() ([]api.Song, error) { return api.SubsonicGetRandomSongs(count*3, genre, fromYear, toYear) })
		}
		fetch(func() ([]api.Song, error) { return api.SubsonicGetRandomSongs(count*2, genre, 0, 0) })
	}

	if len(genres) == 0 && fromYear > 0 { // Untagged genres, fall back to the era
		fetch(func() ([]api.Song, error) { return api.SubsonicGetRandomSongs(count*3, "", fromYear, toYear) })
	}

	// Other albums of the seed artist
	albums := make([]api.Album, 0, len(artistAlbums))
	for _, album := range artistAlbums {
		if album.ID != profile.seedAlbum {
			albums = append(albums, album)
		}
	}
	rand.Shuffle(len(albums), func(i, j int) { albums[i], albums[j] = albums[j], albums[i] })
	for _, album := range albums[:min(mixArtistAlbums, len(albums))] {
		fetch(func() ([]api.Song, error) { return api.SubsonicGetAlbum(album.ID) })
	}

	wg.Wait()
	return candidates
}

// pickMix ranks candidates by similarity and picks a varied selection
func pickMix(profile mixProfile, candidates []api.Song, skip func(api.Song) bool, count int, rng *rand.Rand) []api.Song {
	type rankedSong struct {
		song  api.Song
		score float64
	}

	seen := make(map[string]bool, len(candidates))
	var ranked []rankedSong
	for _, song := range candidates {
		if seen[song.ID] || skip(song) {
			continue
		}
		seen[song.ID] = true

		score := profile.score(song)
		if score < mixMinScore {
			continue
		}

		ranked = append(ranked, rankedSong{song, score + rng.Float64()*mixJitter})
	}

	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	// Limit songs per artist so one artist doesn't take over the mix
	artistCap := max(2, count/4)
	perArtist := map[string]int{}

	var mix []api.Song
	for _, r := range ranked {
		if len(mix) == count {
			break
		}

		artist := r.song.ArtistID
		if artist == "" {
			artist = strings.ToLower(r.song.Artist)
		}
		if perArtist[artist] >= artistCap {
			continue
		}

		perArtist[artist]++
		mix = append(mix, r.song)
	}

	rng.Shuffle(len(mix), func(i, j int) { mix[i], mix[j] = mix[j], mix[i] })
	return mix
}
