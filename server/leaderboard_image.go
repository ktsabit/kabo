package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"kabo/server/persistence"
)

const (
	leaderboardImageWidth  = 1200
	leaderboardTableStart  = 478
	leaderboardTableHeader = 42
	leaderboardRowHeight   = 64
)

var (
	leaderboardTypeface     = loadLeaderboardTypeface(goregular.TTF)
	leaderboardBoldTypeface = loadLeaderboardTypeface(gobold.TTF)
)

func loadLeaderboardTypeface(data []byte) *opentype.Font {
	typeface, err := opentype.Parse(data)
	if err != nil {
		panic("parse embedded leaderboard font: " + err.Error())
	}
	return typeface
}

func renderLeaderboardPNG(serverName string, entries []persistence.LeaderboardEntry, avatars map[string]image.Image) ([]byte, error) {
	if len(entries) > leaderboardSize {
		entries = entries[:leaderboardSize]
	}
	height := leaderboardImageHeight(len(entries))

	canvas := image.NewRGBA(image.Rect(0, 0, leaderboardImageWidth, height))
	drawLeaderboardBackground(canvas)

	panel := image.Rect(28, 28, leaderboardImageWidth-28, height-28)
	fillRoundedRect(canvas, panel, 32, rgba(13, 16, 43, 255))
	fillRoundedRect(canvas, image.Rect(28, 28, 36, 202), 4, rgba(255, 211, 110, 255))

	titleFace, err := leaderboardBoldFace(46)
	if err != nil {
		return nil, err
	}
	metaFace, err := leaderboardFace(20)
	if err != nil {
		return nil, err
	}
	smallBoldFace, err := leaderboardBoldFace(17)
	if err != nil {
		return nil, err
	}
	nameFace, err := leaderboardBoldFace(27)
	if err != nil {
		return nil, err
	}
	scoreFace, err := leaderboardBoldFace(25)
	if err != nil {
		return nil, err
	}

	serverName = cleanLeaderboardText(serverName)
	drawStandingsHeader(canvas, serverName, titleFace, metaFace, smallBoldFace)

	if len(entries) == 0 {
		drawStandingsEmpty(canvas, titleFace, metaFace, smallBoldFace)
		drawLeaderboardFooter(canvas, height, metaFace)
		return encodeLeaderboardPNG(canvas)
	}

	drawStandingsLeader(canvas, entries[0], nameFace, scoreFace, metaFace, smallBoldFace, avatars[entries[0].PlayerID])
	if len(entries) > 1 {
		drawStandingsRunner(canvas, entries[1], 2, 226, nameFace, scoreFace, metaFace, smallBoldFace, avatars[entries[1].PlayerID])
	}
	if len(entries) > 2 {
		drawStandingsRunner(canvas, entries[2], 3, 340, nameFace, scoreFace, metaFace, smallBoldFace, avatars[entries[2].PlayerID])
	}
	if len(entries) == 1 {
		drawStandingsNote(canvas, 226, metaFace, smallBoldFace)
	} else if len(entries) == 2 {
		drawStandingsNote(canvas, 340, metaFace, smallBoldFace)
	}

	if len(entries) > 3 {
		drawStandingsTable(canvas, entries[3:], nameFace, scoreFace, metaFace, smallBoldFace, avatars)
	}

	drawLeaderboardFooter(canvas, height, metaFace)
	return encodeLeaderboardPNG(canvas)
}

func leaderboardImageHeight(entryCount int) int {
	if entryCount <= 0 {
		return 620
	}
	if entryCount <= 3 {
		return 548
	}
	return leaderboardTableStart + leaderboardTableHeader + (entryCount-3)*leaderboardRowHeight + 76
}

func leaderboardFace(size float64) (font.Face, error) {
	return opentype.NewFace(leaderboardTypeface, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

func leaderboardBoldFace(size float64) (font.Face, error) {
	return opentype.NewFace(leaderboardBoldTypeface, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

func drawLeaderboardBackground(canvas *image.RGBA) {
	start := rgba(8, 10, 29, 255)
	end := rgba(12, 13, 34, 255)
	for y := canvas.Bounds().Min.Y; y < canvas.Bounds().Max.Y; y++ {
		progress := float64(y) / float64(canvas.Bounds().Dy()-1)
		rowColor := color.RGBA{
			R: uint8(float64(start.R) + float64(end.R-start.R)*progress),
			G: uint8(float64(start.G) + float64(end.G-start.G)*progress),
			B: uint8(float64(start.B) + float64(end.B-start.B)*progress),
			A: 255,
		}
		draw.Draw(canvas, image.Rect(0, y, canvas.Bounds().Dx(), y+1), image.NewUniform(rowColor), image.Point{}, draw.Src)
	}

}

func drawStandingsHeader(canvas *image.RGBA, serverName string, titleFace, metaFace, labelFace font.Face) {
	drawCompactKaboMark(canvas, 66, 63, labelFace)
	drawText(canvas, "KABO  ·  "+strings.ToUpper(fitLeaderboardText(serverName, labelFace, 480)), 116, 61, labelFace, rgba(164, 169, 202, 255))
	drawText(canvas, "All-time standings", 66, 97, titleFace, rgba(247, 248, 255, 255))
	drawText(canvas, "Most round wins takes the lead.", 68, 157, metaFace, rgba(164, 169, 202, 255))
	drawLeaderboardPill(canvas, "MOST WINS", 962, 72, 170, 42, labelFace, rgba(255, 211, 110, 255), rgba(29, 32, 70, 255))
	fillRoundedRect(canvas, image.Rect(66, 200, 1134, 202), 1, rgba(40, 44, 82, 255))
}

func drawCompactKaboMark(canvas *image.RGBA, x, y int, face font.Face) {
	fillRoundedRect(canvas, image.Rect(x+4, y-3, x+37, y+39), 7, rgba(66, 75, 198, 255))
	fillRoundedRect(canvas, image.Rect(x, y+1, x+33, y+43), 7, rgba(246, 247, 255, 255))
	fillRoundedRect(canvas, image.Rect(x+4, y+5, x+29, y+39), 5, rgba(38, 44, 122, 255))
	drawCenteredTextAt(canvas, "K", x+16, y+10, face, rgba(255, 211, 110, 255))
}

func drawStandingsLeader(canvas *image.RGBA, entry persistence.LeaderboardEntry, nameFace, scoreFace, metaFace, labelFace font.Face, avatar image.Image) {
	card := image.Rect(66, 226, 742, 454)
	fillRoundedRect(canvas, card, 22, rgba(24, 28, 71, 255))
	fillRoundedRect(canvas, image.Rect(card.Min.X, card.Min.Y, card.Min.X+5, card.Max.Y), 3, rgba(255, 211, 110, 255))
	drawText(canvas, "01", 96, 252, scoreFace, rgba(255, 211, 110, 255))
	drawText(canvas, "LEADER", 97, 291, labelFace, rgba(164, 169, 202, 255))

	drawCircle(canvas, 192, 363, 61, rgba(255, 211, 110, 255))
	drawLeaderboardAvatar(canvas, 192, 363, 55, entry.DisplayName, avatar, rgba(48, 57, 150, 255), rgba(247, 248, 255, 255), 34)

	name := fitLeaderboardText(cleanLeaderboardText(entry.DisplayName), nameFace, 390)
	drawText(canvas, name, 282, 268, nameFace, rgba(247, 248, 255, 255))
	drawText(canvas, "ROUND WINS", 283, 311, labelFace, rgba(164, 169, 202, 255))
	largeScoreFace, err := leaderboardBoldFace(62)
	if err == nil {
		wins := itoa(entry.Wins)
		drawText(canvas, wins, 280, 337, largeScoreFace, rgba(255, 211, 110, 255))
		drawText(canvas, "WINS", 295+font.MeasureString(largeScoreFace, wins).Ceil(), 374, scoreFace, rgba(255, 211, 110, 255))
	}
	drawText(canvas, formatLeaderboardPercent(entry.WinRate)+" win rate   ·   "+metricLabel(entry.Games, "round", "rounds"), 283, 414, metaFace, rgba(188, 192, 218, 255))
}

func drawStandingsRunner(canvas *image.RGBA, entry persistence.LeaderboardEntry, rank, y int, nameFace, scoreFace, metaFace, labelFace font.Face, avatar image.Image) {
	card := image.Rect(760, y, 1134, y+104)
	fillRoundedRect(canvas, card, 18, rgba(21, 25, 62, 255))
	drawText(canvas, "0"+itoa(rank), 784, y+22, scoreFace, rgba(170, 176, 218, 255))
	drawLeaderboardAvatar(canvas, 850, y+52, 34, entry.DisplayName, avatar, rgba(48, 57, 150, 255), rgba(247, 248, 255, 255), 21)
	runnerNameFace := nameFace
	if compactFace, err := leaderboardBoldFace(23); err == nil {
		runnerNameFace = compactFace
	}
	name := fitLeaderboardText(cleanLeaderboardText(entry.DisplayName), runnerNameFace, 166)
	drawText(canvas, name, 901, y+18, runnerNameFace, rgba(247, 248, 255, 255))
	drawText(canvas, metricLabel(entry.Games, "round", "rounds")+" · "+formatLeaderboardPercent(entry.WinRate), 902, y+60, labelFace, rgba(150, 156, 194, 255))
	drawRightText(canvas, itoa(entry.Wins), 1110, y+21, scoreFace, rgba(255, 211, 110, 255))
	drawRightText(canvas, "WINS", 1110, y+60, labelFace, rgba(150, 156, 194, 255))
}

func drawStandingsNote(canvas *image.RGBA, y int, metaFace, labelFace font.Face) {
	card := image.Rect(760, y, 1134, y+104)
	fillRoundedRect(canvas, card, 18, rgba(21, 25, 62, 255))
	drawText(canvas, "SCORING", 786, y+22, labelFace, rgba(164, 169, 202, 255))
	drawText(canvas, "Every round win moves you", 786, y+52, metaFace, rgba(230, 232, 246, 255))
	drawText(canvas, "up the standings.", 786, y+77, metaFace, rgba(230, 232, 246, 255))
}

func drawStandingsTable(canvas *image.RGBA, entries []persistence.LeaderboardEntry, nameFace, scoreFace, metaFace, labelFace font.Face, avatars map[string]image.Image) {
	tableBottom := leaderboardTableStart + leaderboardTableHeader + len(entries)*leaderboardRowHeight
	fillRoundedRect(canvas, image.Rect(66, leaderboardTableStart, 1134, tableBottom), 18, rgba(18, 22, 55, 255))
	drawText(canvas, "RANK", 88, leaderboardTableStart+12, labelFace, rgba(132, 138, 178, 255))
	drawText(canvas, "PLAYER", 184, leaderboardTableStart+12, labelFace, rgba(132, 138, 178, 255))
	drawRightText(canvas, "ROUNDS", 846, leaderboardTableStart+12, labelFace, rgba(132, 138, 178, 255))
	drawRightText(canvas, "WIN RATE", 1000, leaderboardTableStart+12, labelFace, rgba(132, 138, 178, 255))
	drawRightText(canvas, "WINS", 1110, leaderboardTableStart+12, labelFace, rgba(132, 138, 178, 255))

	for index, entry := range entries {
		y := leaderboardTableStart + leaderboardTableHeader + index*leaderboardRowHeight
		if index%2 == 0 {
			fillRoundedRect(canvas, image.Rect(68, y, 1132, y+leaderboardRowHeight), 0, rgba(21, 25, 62, 255))
		}
		if index > 0 {
			fillRoundedRect(canvas, image.Rect(88, y, 1112, y+1), 0, rgba(38, 42, 76, 255))
		}
		rank := index + 4
		drawText(canvas, twoDigitRank(rank), 91, y+20, metaFace, rgba(164, 169, 202, 255))
		drawLeaderboardAvatar(canvas, 157, y+leaderboardRowHeight/2, 22, entry.DisplayName, avatars[entry.PlayerID], rgba(48, 57, 150, 255), rgba(247, 248, 255, 255), 15)
		name := fitLeaderboardText(cleanLeaderboardText(entry.DisplayName), nameFace, 430)
		drawText(canvas, name, 190, y+15, nameFace, rgba(239, 241, 251, 255))
		drawRightText(canvas, itoa(entry.Games), 846, y+20, metaFace, rgba(188, 192, 218, 255))
		drawRightText(canvas, formatLeaderboardPercent(entry.WinRate), 1000, y+20, metaFace, rgba(188, 192, 218, 255))
		drawRightText(canvas, itoa(entry.Wins), 1110, y+16, scoreFace, rgba(255, 211, 110, 255))
	}
}

func drawStandingsEmpty(canvas *image.RGBA, titleFace, metaFace, labelFace font.Face) {
	card := image.Rect(66, 226, 1134, 526)
	fillRoundedRect(canvas, card, 22, rgba(18, 22, 55, 255))
	drawCompactKaboMark(canvas, 112, 292, labelFace)
	drawText(canvas, "NO SCORES YET", 112, 354, labelFace, rgba(255, 211, 110, 255))
	drawText(canvas, "Finish the first round", 316, 282, titleFace, rgba(247, 248, 255, 255))
	drawText(canvas, "Completed Discord Activity rounds appear here automatically.", 318, 347, metaFace, rgba(164, 169, 202, 255))
	drawText(canvas, "The first winner takes the lead; future wins move the table.", 318, 383, metaFace, rgba(164, 169, 202, 255))
}

func drawLeaderboardPill(canvas *image.RGBA, label string, x, y, width, height int, face font.Face, foreground, background color.RGBA) {
	fillRoundedRect(canvas, image.Rect(x, y, x+width, y+height), height/2, background)
	textWidth := font.MeasureString(face, label).Ceil()
	drawText(canvas, label, x+(width-textWidth)/2, y+(height-face.Metrics().Height.Ceil())/2, face, foreground)
}

func drawLeaderboardFooter(canvas *image.RGBA, height int, face font.Face) {
	drawText(canvas, "KABO", 58, height-52, face, rgba(245, 246, 255, 255))
	drawRightText(canvas, "ALL-TIME  /  TOTAL ROUND WINS  /  HIGHER IS BETTER", leaderboardImageWidth-58, height-52, face, rgba(167, 172, 210, 255))
}

func formatLeaderboardPercent(value float64) string {
	return strconv.FormatFloat(value, 'f', 0, 64) + "%"
}

func metricLabel(value int, singular, plural string) string {
	label := plural
	if value == 1 {
		label = singular
	}
	return itoa(value) + " " + label
}

func twoDigitRank(rank int) string {
	if rank < 10 {
		return "0" + itoa(rank)
	}
	return itoa(rank)
}

func drawLeaderboardAvatar(canvas *image.RGBA, x, y, radius int, name string, avatar image.Image, background, foreground color.RGBA, fontSize float64) {
	drawCircle(canvas, x, y, radius, background)
	if avatar != nil {
		drawCircularImage(canvas, x, y, radius-2, avatar)
		return
	}
	face, err := leaderboardFace(fontSize)
	if err != nil {
		return
	}
	initial := leaderboardInitial(name)
	drawCenteredTextAt(canvas, initial, x, y-radius/2, face, foreground)
}

func drawCircularImage(canvas *image.RGBA, centerX, centerY, radius int, source image.Image) {
	bounds := source.Bounds()
	sourceWidth := bounds.Dx()
	sourceHeight := bounds.Dy()
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return
	}
	sourceSize := sourceWidth
	if sourceHeight < sourceSize {
		sourceSize = sourceHeight
	}
	startX := bounds.Min.X + (sourceWidth-sourceSize)/2
	startY := bounds.Min.Y + (sourceHeight-sourceSize)/2
	for y := -radius; y <= radius; y++ {
		width := intSqrt(radius*radius - y*y)
		for x := -width; x <= width; x++ {
			sourceX := startX + (x+radius)*sourceSize/(radius*2+1)
			sourceY := startY + (y+radius)*sourceSize/(radius*2+1)
			canvas.Set(centerX+x, centerY+y, source.At(sourceX, sourceY))
		}
	}
}

func leaderboardInitial(name string) string {
	name = strings.TrimSpace(name)
	for _, runeValue := range name {
		return strings.ToUpper(string(runeValue))
	}
	return "?"
}

func cleanLeaderboardText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "Player"
	}
	return value
}

func fitLeaderboardText(value string, face font.Face, maxWidth int) string {
	value = cleanLeaderboardText(value)
	if font.MeasureString(face, value).Ceil() <= maxWidth {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 {
		candidate := string(runes) + "…"
		if font.MeasureString(face, candidate).Ceil() <= maxWidth {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return "…"
}

func drawText(canvas *image.RGBA, value string, x, y int, face font.Face, foreground color.Color) {
	drawer := font.Drawer{
		Dst:  canvas,
		Src:  image.NewUniform(foreground),
		Face: face,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y) + face.Metrics().Ascent},
	}
	drawer.DrawString(value)
}

func drawRightText(canvas *image.RGBA, value string, right, y int, face font.Face, foreground color.Color) {
	width := font.MeasureString(face, value).Ceil()
	drawText(canvas, value, right-width, y, face, foreground)
}

func drawCenteredTextAt(canvas *image.RGBA, value string, center, y int, face font.Face, foreground color.Color) {
	width := font.MeasureString(face, value).Ceil()
	drawText(canvas, value, center-width/2, y, face, foreground)
}

func fillRoundedRect(canvas draw.Image, rectangle image.Rectangle, radius int, foreground color.Color) {
	if radius <= 0 {
		draw.Draw(canvas, rectangle, image.NewUniform(foreground), image.Point{}, draw.Over)
		return
	}
	if radius*2 > rectangle.Dx() {
		radius = rectangle.Dx() / 2
	}
	if radius*2 > rectangle.Dy() {
		radius = rectangle.Dy() / 2
	}
	source := image.NewUniform(foreground)
	draw.Draw(canvas, image.Rect(rectangle.Min.X+radius, rectangle.Min.Y, rectangle.Max.X-radius, rectangle.Max.Y), source, image.Point{}, draw.Over)
	draw.Draw(canvas, image.Rect(rectangle.Min.X, rectangle.Min.Y+radius, rectangle.Max.X, rectangle.Max.Y-radius), source, image.Point{}, draw.Over)
	drawCircle(canvas, rectangle.Min.X+radius, rectangle.Min.Y+radius, radius, foreground)
	drawCircle(canvas, rectangle.Max.X-radius-1, rectangle.Min.Y+radius, radius, foreground)
	drawCircle(canvas, rectangle.Min.X+radius, rectangle.Max.Y-radius-1, radius, foreground)
	drawCircle(canvas, rectangle.Max.X-radius-1, rectangle.Max.Y-radius-1, radius, foreground)
}

func drawCircle(canvas draw.Image, centerX, centerY, radius int, foreground color.Color) {
	source := image.NewUniform(foreground)
	for y := -radius; y <= radius; y++ {
		width := intSqrt(radius*radius - y*y)
		draw.Draw(canvas, image.Rect(centerX-width, centerY+y, centerX+width+1, centerY+y+1), source, image.Point{}, draw.Over)
	}
}

func intSqrt(value int) int {
	if value <= 0 {
		return 0
	}
	low, high := 0, value
	for low <= high {
		middle := low + (high-low)/2
		if middle != 0 && middle > value/middle {
			high = middle - 1
			continue
		}
		if middle*middle == value {
			return middle
		}
		low = middle + 1
	}
	return high
}

func encodeLeaderboardPNG(canvas *image.RGBA) ([]byte, error) {
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func rgba(red, green, blue, alpha uint8) color.RGBA {
	return color.RGBA{R: red, G: green, B: blue, A: alpha}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
