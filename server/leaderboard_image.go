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
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"kabo/server/persistence"
)

const (
	leaderboardImageWidth = 1200
	leaderboardRowStart   = 440
	leaderboardRowHeight  = 70
	leaderboardRowGap     = 12
)

var leaderboardTypeface = loadLeaderboardTypeface()

func loadLeaderboardTypeface() *opentype.Font {
	typeface, err := opentype.Parse(goregular.TTF)
	if err != nil {
		panic("parse embedded leaderboard font: " + err.Error())
	}
	return typeface
}

func renderLeaderboardPNG(serverName string, entries []persistence.LeaderboardEntry, avatars map[string]image.Image) ([]byte, error) {
	if len(entries) > leaderboardSize {
		entries = entries[:leaderboardSize]
	}
	rows := 0
	if len(entries) > 3 {
		rows = len(entries) - 3
	}
	height := leaderboardRowStart + 42
	if rows > 0 {
		height += rows * (leaderboardRowHeight + leaderboardRowGap)
	}
	if len(entries) == 0 {
		height = 500
	}

	canvas := image.NewRGBA(image.Rect(0, 0, leaderboardImageWidth, height))
	drawLeaderboardBackground(canvas)

	panel := image.Rect(26, 26, leaderboardImageWidth-26, height-26)
	fillRoundedRect(canvas, panel, 48, rgba(13, 38, 54, 255))
	fillRoundedRect(canvas, image.Rect(26, 26, leaderboardImageWidth-26, 188), 48, rgba(20, 53, 71, 255))

	titleFace, err := leaderboardFace(48)
	if err != nil {
		return nil, err
	}
	metaFace, err := leaderboardFace(22)
	if err != nil {
		return nil, err
	}
	nameFace, err := leaderboardFace(25)
	if err != nil {
		return nil, err
	}
	scoreFace, err := leaderboardFace(23)
	if err != nil {
		return nil, err
	}
	rowNameFace, err := leaderboardFace(27)
	if err != nil {
		return nil, err
	}

	drawCenteredText(canvas, "KABO LEADERBOARD", 58, titleFace, rgba(255, 211, 110, 255))
	drawCenteredText(canvas, "SERVER: "+fitLeaderboardText(cleanLeaderboardText(serverName), metaFace, 560), 128, metaFace, rgba(221, 230, 244, 255))

	if len(entries) == 0 {
		drawCenteredText(canvas, "NO COMPLETED ROUNDS YET", 285, nameFace, rgba(245, 246, 255, 255))
		drawCenteredText(canvas, "Play a round to enter the leaderboard", 330, metaFace, rgba(167, 172, 210, 255))
		drawLeaderboardFooter(canvas, height, metaFace)
		return encodeLeaderboardPNG(canvas)
	}

	topPositions := leaderboardTopPositions(len(entries))
	for index, position := range topPositions {
		drawLeaderboardTopPlayer(canvas, entries[index], index, position, nameFace, scoreFace, avatars[entries[index].PlayerID])
	}

	for index := 3; index < len(entries); index++ {
		rowIndex := index - 3
		y := leaderboardRowStart + rowIndex*(leaderboardRowHeight+leaderboardRowGap)
		rowColor := rgba(21, 58, 80, 255)
		if rowIndex%2 == 1 {
			rowColor = rgba(18, 72, 103, 255)
		}
		fillRoundedRect(canvas, image.Rect(48, y, leaderboardImageWidth-48, y+leaderboardRowHeight), 22, rowColor)

		drawLeaderboardAvatar(canvas, 100, y+leaderboardRowHeight/2, 27, entries[index].DisplayName, avatars[entries[index].PlayerID], rgba(42, 52, 180, 255), rgba(245, 246, 255, 255), 20)
		drawText(canvas, "#"+itoa(index+1), 154, y+21, scoreFace, rgba(245, 246, 255, 255))

		name := fitLeaderboardText(cleanLeaderboardText(entries[index].DisplayName), rowNameFace, 690)
		drawText(canvas, name, 245, y+20, rowNameFace, rgba(245, 246, 255, 255))
		score := itoa(entries[index].TotalScore) + " pts"
		drawRightText(canvas, score, 1048, y+21, scoreFace, rgba(255, 211, 110, 255))
	}

	drawLeaderboardFooter(canvas, height, metaFace)
	return encodeLeaderboardPNG(canvas)
}

func leaderboardFace(size float64) (font.Face, error) {
	return opentype.NewFace(leaderboardTypeface, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

func drawLeaderboardBackground(canvas *image.RGBA) {
	start := rgba(7, 10, 32, 255)
	end := rgba(20, 12, 45, 255)
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

	// Subtle card shapes echo the game table without competing with the rows.
	fillRoundedRect(canvas, image.Rect(930, -25, 1110, 190), 26, rgba(48, 57, 185, 80))
	fillRoundedRect(canvas, image.Rect(1010, 42, 1195, 260), 26, rgba(89, 100, 228, 45))
}

func drawLeaderboardTopPlayer(canvas *image.RGBA, entry persistence.LeaderboardEntry, index, x int, nameFace, scoreFace font.Face, avatar image.Image) {
	ringColor := rgba(255, 211, 110, 255)
	if index == 1 {
		ringColor = rgba(202, 211, 224, 255)
	} else if index == 2 {
		ringColor = rgba(211, 135, 62, 255)
	}
	drawCircle(canvas, x, 250, 78, ringColor)
	drawCircle(canvas, x, 250, 65, rgba(31, 43, 64, 255))
	drawLeaderboardAvatar(canvas, x, 250, 58, entry.DisplayName, avatar, rgba(48, 57, 185, 255), rgba(245, 246, 255, 255), 40)

	rank := []string{"1ST", "2ND", "3RD"}[index]
	drawCenteredTextAt(canvas, rank, x, 342, scoreFace, rgba(245, 246, 255, 255))
	name := fitLeaderboardText(cleanLeaderboardText(entry.DisplayName), nameFace, 190)
	drawCenteredTextAt(canvas, name, x, 372, nameFace, rgba(255, 211, 110, 255))
	drawCenteredTextAt(canvas, itoa(entry.TotalScore)+" pts · "+itoa(entry.Wins)+" wins", x, 407, scoreFace, rgba(221, 230, 244, 255))
}

func leaderboardTopPositions(count int) []int {
	switch count {
	case 1:
		return []int{leaderboardImageWidth / 2}
	case 2:
		return []int{leaderboardImageWidth/2 - 125, leaderboardImageWidth/2 + 125}
	default:
		return []int{leaderboardImageWidth / 2, leaderboardImageWidth/2 - 235, leaderboardImageWidth/2 + 235}
	}
}

func drawLeaderboardFooter(canvas *image.RGBA, height int, face font.Face) {
	drawCenteredText(canvas, "CUMULATIVE POINTS · LOWER IS BETTER", height-34, face, rgba(167, 172, 210, 255))
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

func drawCenteredText(canvas *image.RGBA, value string, y int, face font.Face, foreground color.Color) {
	drawCenteredTextAt(canvas, value, leaderboardImageWidth/2, y, face, foreground)
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
