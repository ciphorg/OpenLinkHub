package colorshift

import (
	"OpenICUELinkHub/src/device/brightness"
	"OpenICUELinkHub/src/device/comm"
	"OpenICUELinkHub/src/device/common"
	"OpenICUELinkHub/src/device/opcodes"
	"OpenICUELinkHub/src/structs"
	"time"
)

var exit = make(chan bool)

// interpolateColor performs linear interpolation between two colors
func interpolateColor(c1, c2 *structs.Color, t float64) *structs.Color {
	return &structs.Color{
		Red:   common.Lerp(c1.Red, c2.Red, t),
		Green: common.Lerp(c1.Green, c2.Green, t),
		Blue:  common.Lerp(c1.Blue, c2.Blue, t),
	}
}

// generateColors will generate color based on start and end color
func generateColors(
	lightChannels int,
	c1,
	c2 *structs.Color,
	factor,
	bts float64,
) []struct{ R, G, B float64 } {
	colors := make([]struct{ R, G, B float64 }, lightChannels)
	for i := 0; i < lightChannels; i++ {
		color := interpolateColor(c1, c2, factor)
		color.Brightness = bts
		modify := brightness.ModifyBrightness(*color)
		colors[i] = struct{ R, G, B float64 }{modify.Red, modify.Green, modify.Blue}
	}
	return colors
}

func Stop() {
	exit <- true
}

// Init will run RGB function
func Init(
	lightChannels,
	smoothness int,
	rgbCustomColor bool,
	rgbLoopDuration time.Duration,
	rgbStartColor,
	rgbEndColor *structs.Color,
	bts float64,
) {
	buf := map[int][]byte{}

	if !rgbCustomColor {
		rgbStartColor = common.GenerateRandomColor(bts)
		rgbEndColor = common.GenerateRandomColor(bts)
	}

	for {
		select {
		case <-exit:
			return
		default:
			// Initial
			for i := 0; i <= smoothness; i++ {
				t := float64(i) / float64(smoothness) // Calculate interpolation factor
				colors := generateColors(lightChannels, rgbStartColor, rgbEndColor, t, bts)

				// Update LED channels
				for j, color := range colors {
					buf[j] = []byte{
						byte(color.R),
						byte(color.G),
						byte(color.B),
					}
				}
				select {
				case <-exit:
					return
				case <-time.After(40 * time.Millisecond):
					data := common.SetColor(buf)
					comm.WriteColor(opcodes.DataTypeSetColor, data)
				}
			}

			select {
			case <-exit:
				return
			case <-time.After(rgbLoopDuration):
			}

			// Reverse
			for i := 0; i <= smoothness; i++ {
				t := float64(i) / float64(smoothness) // Calculate interpolation factor
				colors := generateColors(lightChannels, rgbEndColor, rgbStartColor, t, bts)

				// Update LED channels
				for j, color := range colors {
					buf[j] = []byte{
						byte(color.R),
						byte(color.G),
						byte(color.B),
					}
				}
				select {
				case <-exit:
					return
				case <-time.After(40 * time.Millisecond):
					data := common.SetColor(buf)
					comm.WriteColor(opcodes.DataTypeSetColor, data)
				}
			}
		}
	}
}
