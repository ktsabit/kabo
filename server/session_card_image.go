package main

import (
	"image"
	"image/draw"
	"strings"

	"golang.org/x/image/font"

	"kabo/server/game"
	"kabo/server/transport"
)

const sessionCardImageWidth = 1000

func renderSessionCardPNG(update transport.RoomUpdate, avatarSets ...map[string]image.Image) ([]byte, error) {
	players := sessionCardPlayers(update)
	var avatars map[string]image.Image
	if len(avatarSets) > 0 {
		avatars = avatarSets[0]
	}
	rows := (len(players) + 1) / 2
	if rows < 1 {
		rows = 1
	}
	height := 258 + rows*92 + 28
	canvas := image.NewRGBA(image.Rect(0, 0, sessionCardImageWidth, height))
	drawKaboLeaderboardBackground(canvas)

	titleFace, err := leaderboardBoldFace(31)
	if err != nil {
		return nil, err
	}
	statusFace, err := leaderboardBoldFace(42)
	if err != nil {
		return nil, err
	}
	subtitleFace, err := leaderboardFace(22)
	if err != nil {
		return nil, err
	}
	nameFace, err := leaderboardBoldFace(23)
	if err != nil {
		return nil, err
	}
	metaFace, err := leaderboardFace(17)
	if err != nil {
		return nil, err
	}

	drawText(canvas, "Kabo", 44, 34, titleFace, rgba(246, 247, 251, 255))
	badge := sessionCardBadge(update)
	badgeWidth := font.MeasureString(metaFace, badge).Ceil() + 38
	fillRoundedRect(canvas, image.Rect(956-badgeWidth, 37, 956, 76), 19, rgba(48, 57, 185, 255))
	drawCenteredTextAt(canvas, badge, 956-badgeWidth/2, 47, metaFace, rgba(245, 246, 255, 255))

	status, subtitle := sessionCardStatus(update, len(players))
	drawText(canvas, fitLeaderboardText(status, statusFace, 912), 44, 103, statusFace, rgba(246, 247, 251, 255))
	drawText(canvas, subtitle, 44, 164, subtitleFace, rgba(169, 176, 255, 255))
	draw.Draw(canvas, image.Rect(44, 216, 956, 218), image.NewUniform(rgba(42, 48, 112, 255)), image.Point{}, draw.Src)

	winners := make(map[string]bool, len(update.WinnerIDs))
	for _, winnerID := range update.WinnerIDs {
		winners[winnerID] = true
	}
	for index, player := range players {
		column := index % 2
		row := index / 2
		x := 44 + column*466
		y := 246 + row*92
		background := rgba(21, 25, 70, 255)
		accent := rgba(89, 100, 228, 255)
		meta := "Playing"
		if !player.Connected {
			background = rgba(17, 20, 53, 255)
			accent = rgba(88, 92, 120, 255)
			meta = "Away"
		}
		if winners[player.ID] {
			background = rgba(55, 38, 93, 255)
			accent = rgba(255, 159, 154, 255)
			meta = "Round winner"
		}
		fillRoundedRect(canvas, image.Rect(x, y, x+446, y+72), 17, background)
		drawLeaderboardAvatar(canvas, x+40, y+36, 25, player.Name, avatars[player.ID], accent, rgba(245, 246, 255, 255), 16)
		drawText(canvas, fitLeaderboardText(player.Name, nameFace, 280), x+82, y+12, nameFace, rgba(246, 247, 251, 255))
		drawRightText(canvas, meta, x+420, y+18, metaFace, rgba(169, 176, 255, 255))
	}

	return encodeLeaderboardPNG(canvas)
}

func sessionCardPlayers(update transport.RoomUpdate) []transport.RoomPlayer {
	players := make([]transport.RoomPlayer, 0, len(update.Players))
	for _, player := range update.Players {
		player.Name = cleanLeaderboardText(player.Name)
		players = append(players, player)
	}
	return players
}

func sessionCardBadge(update transport.RoomUpdate) string {
	connected := 0
	for _, player := range update.Players {
		if player.Connected {
			connected++
		}
	}
	if connected == 1 {
		return "1 PLAYING"
	}
	return itoa(connected) + " PLAYING"
}

func sessionCardStatus(update transport.RoomUpdate, playerCount int) (string, string) {
	switch update.Phase {
	case game.PhaseLobby:
		return "A table is open", playerCountLabel(playerCount) + " · ready up when everyone is here"
	case game.PhaseEnded:
		if names := sessionWinnerNames(update); names != "" {
			return names + " won round " + itoa(update.Round), playerCountLabel(playerCount) + " · the next round is open"
		}
		return "Round " + itoa(update.Round) + " complete", playerCountLabel(playerCount) + " · the next round is open"
	default:
		return "Round " + itoa(update.Round) + " is live", playerCountLabel(playerCount) + " at the table"
	}
}

func playerCountLabel(count int) string {
	if count == 1 {
		return "1 player"
	}
	return itoa(count) + " players"
}

func sessionWinnerNames(update transport.RoomUpdate) string {
	winners := make(map[string]bool, len(update.WinnerIDs))
	for _, id := range update.WinnerIDs {
		winners[id] = true
	}
	names := make([]string, 0, len(winners))
	for _, player := range update.Players {
		if winners[player.ID] {
			names = append(names, cleanLeaderboardText(player.Name))
		}
	}
	return strings.Join(names, " & ")
}
