package main

// Channel is one IPTV stream (name + URL).
type Channel struct {
	Name string
	URL  string
}

// SourceResult holds one tested API host and its channels.
type SourceResult struct {
	Host      string
	MatchType string
	Source    string
	Speed     float64
	Channels  []Channel
}

// Entry is a fully resolved playlist line ready for output.
type Entry struct {
	Name    string
	URL     string
	Content string  // full #EXTINF + URL block
	Index   int     // lower = higher priority source
	Speed   float64 // measured MB/s (0 if untested subscribe channel discarded by host)
}
