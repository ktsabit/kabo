package main

import (
	"bytes"
	"image/png"
	"strings"
	"testing"

	"kabo/server/persistence"
)

func TestRenderLeaderboardPNGProducesCardForTopTen(t *testing.T) {
	entries := make([]persistence.LeaderboardEntry, 10)
	for index := range entries {
		entries[index] = persistence.LeaderboardEntry{
			PlayerID:    "player-" + itoa(index+1),
			DisplayName: strings.Repeat("Player ", 2) + itoa(index+1),
			Games:       index + 1,
			Wins:        index,
			TotalScore:  index * 3,
		}
	}

	data, err := renderLeaderboardPNG("Kabo Test Server", entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	image, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if image.Bounds().Dx() != leaderboardImageWidth {
		t.Fatalf("rendered width = %d, want %d", image.Bounds().Dx(), leaderboardImageWidth)
	}
	wantHeight := leaderboardRowStart + 42 + 7*(leaderboardRowHeight+leaderboardRowGap)
	if image.Bounds().Dy() != wantHeight {
		t.Fatalf("rendered height = %d, want %d", image.Bounds().Dy(), wantHeight)
	}
}
