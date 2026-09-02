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
	leaderboardImageWidth = 1000
	leaderboardListStart  = 410
	leaderboardPageStart  = 104
	leaderboardRowHeight  = 74
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

func renderLeaderboardPNG(_ string, entries []persistence.LeaderboardEntry, avatars map[string]image.Image) ([]byte, error) {
	return renderLeaderboardPagePNG(entries, avatars, 0, "", "")
}

func renderLeaderboardPagePNG(entries []persistence.LeaderboardEntry, avatars map[string]image.Image, rankOffset int, viewerID, viewerName string) ([]byte, error) {
	if len(entries) > leaderboardSize {
		entries = entries[:leaderboardSize]
	}
	height := leaderboardPageImageHeight(len(entries), rankOffset)

	canvas := image.NewRGBA(image.Rect(0, 0, leaderboardImageWidth, height))
	drawKaboLeaderboardBackground(canvas)

	titleFace, err := leaderboardBoldFace(36)
	if err != nil {
		return nil, err
	}
	rankFace, err := leaderboardBoldFace(20)
	if err != nil {
		return nil, err
	}
	nameFace, err := leaderboardBoldFace(25)
	if err != nil {
		return nil, err
	}
	valueFace, err := leaderboardFace(22)
	if err != nil {
		return nil, err
	}

	drawCenteredTextAt(canvas, "Kabo Leaderboard", leaderboardImageWidth/2, 30, titleFace, rgba(246, 247, 251, 255))

	if len(entries) == 0 {
		drawCenteredTextAt(canvas, "No completed rounds yet", leaderboardImageWidth/2, 132, nameFace, rgba(155, 159, 178, 255))
		return encodeLeaderboardPNG(canvas)
	}

	startIndex := 0
	rowY := leaderboardPageStart
	if rankOffset == 0 {
		podiumCount := len(entries)
		if podiumCount > 3 {
			podiumCount = 3
		}
		drawLeaderboardPodium(canvas, entries[:podiumCount], avatars, viewerID, viewerName, nameFace, valueFace, rankFace)
		startIndex = podiumCount
		rowY = leaderboardListStart
	}
	for index := startIndex; index < len(entries); index++ {
		entry := entries[index]
		drawLeaderboardRow(canvas, entry, rankOffset+index+1, rowY+(index-startIndex)*leaderboardRowHeight, viewerID, viewerName, nameFace, valueFace, rankFace, avatars[entry.PlayerID])
	}

	return encodeLeaderboardPNG(canvas)
}

func leaderboardImageHeight(entryCount int) int {
	return leaderboardPageImageHeight(entryCount, 0)
}

func leaderboardPageImageHeight(entryCount, rankOffset int) int {
	if entryCount <= 0 {
		return 230
	}
	if rankOffset == 0 {
		if entryCount <= 3 {
			return 400
		}
		return leaderboardListStart + (entryCount-3)*leaderboardRowHeight + 22
	}
	return leaderboardPageStart + entryCount*leaderboardRowHeight + 22
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

func drawKaboLeaderboardBackground(canvas *image.RGBA) {
	start := rgba(13, 16, 51, 255)
	middle := rgba(8, 10, 32, 255)
	end := rgba(16, 12, 45, 255)
	for y := canvas.Bounds().Min.Y; y < canvas.Bounds().Max.Y; y++ {
		progress := float64(y) / float64(canvas.Bounds().Dy()-1)
		from, to, localProgress := start, middle, progress/0.54
		if progress > 0.54 {
			from, to, localProgress = middle, end, (progress-0.54)/0.46
		}
		rowColor := color.RGBA{
			R: uint8(float64(from.R) + (float64(to.R)-float64(from.R))*localProgress),
			G: uint8(float64(from.G) + (float64(to.G)-float64(from.G))*localProgress),
			B: uint8(float64(from.B) + (float64(to.B)-float64(from.B))*localProgress),
			A: 255,
		}
		draw.Draw(canvas, image.Rect(0, y, canvas.Bounds().Dx(), y+1), image.NewUniform(rowColor), image.Point{}, draw.Src)
	}
}

func drawLeaderboardPodium(canvas *image.RGBA, entries []persistence.LeaderboardEntry, avatars map[string]image.Image, viewerID, viewerName string, nameFace, valueFace, rankFace font.Face) {
	positions := []int{500}
	if len(entries) == 2 {
		positions = []int{360, 650}
	} else if len(entries) >= 3 {
		positions = []int{500, 225, 775}
	}

	for index, entry := range entries {
		rank := index + 1
		centerY, radius := 205, 62
		if rank == 1 {
			centerY, radius = 178, 76
		}
		accent := leaderboardRankAccent(rank)
		accentText := leaderboardRankAccentText(rank)
		name := leaderboardDisplayName(entry, viewerID, viewerName)
		x := positions[index]
		drawLeaderboardAvatar(canvas, x, centerY, radius, name, avatars[entry.PlayerID], accent, accentText, 30)
		rankY := centerY + radius - 6
		drawCircle(canvas, x, rankY, 24, accent)
		drawCenteredTextAt(canvas, itoa(rank), x, rankY-12, rankFace, accentText)
		drawCenteredTextAt(canvas, fitLeaderboardText(name, nameFace, 240), x, centerY+radius+31, nameFace, rgba(246, 247, 251, 255))
		drawCenteredTextAt(canvas, winsLabel(entry.Wins), x, centerY+radius+69, valueFace, rgba(169, 176, 255, 255))
	}
}

func drawLeaderboardRow(canvas *image.RGBA, entry persistence.LeaderboardEntry, rank, y int, viewerID, viewerName string, nameFace, valueFace, rankFace font.Face, avatar image.Image) {
	highlighted := viewerID != "" && entry.PlayerID == viewerID
	background := rgba(21, 25, 70, 255)
	if highlighted {
		background = rgba(48, 57, 185, 255)
	}
	fillRoundedRect(canvas, image.Rect(28, y, 972, y+62), 16, background)

	name := leaderboardDisplayName(entry, viewerID, viewerName)
	drawText(canvas, itoa(rank), 56, y+18, rankFace, rgba(245, 246, 249, 255))
	drawLeaderboardAvatar(canvas, 126, y+31, 25, name, avatar, leaderboardRankAccent(rank), leaderboardRankAccentText(rank), 16)
	drawText(canvas, fitLeaderboardText(name, nameFace, 610), 170, y+15, nameFace, rgba(246, 247, 251, 255))
	drawRightText(canvas, winsLabel(entry.Wins), 936, y+18, valueFace, rgba(169, 176, 255, 255))
}

func leaderboardRankAccent(rank int) color.RGBA {
	accents := []color.RGBA{
		rgba(89, 100, 228, 255),
		rgba(143, 218, 238, 255),
		rgba(255, 159, 154, 255),
		rgba(201, 184, 255, 255),
		rgba(240, 169, 228, 255),
	}
	return accents[(rank-1)%len(accents)]
}

func leaderboardRankAccentText(rank int) color.RGBA {
	if (rank-1)%5 == 0 {
		return rgba(245, 246, 255, 255)
	}
	return rgba(8, 10, 32, 255)
}

func leaderboardDisplayName(entry persistence.LeaderboardEntry, viewerID, viewerName string) string {
	if viewerID != "" && entry.PlayerID == viewerID && strings.TrimSpace(viewerName) != "" {
		return cleanLeaderboardText(viewerName)
	}
	return cleanLeaderboardText(entry.DisplayName)
}

func winsLabel(wins int) string {
	if wins == 1 {
		return "1 win"
	}
	return itoa(wins) + " wins"
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
