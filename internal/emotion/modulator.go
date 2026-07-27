package emotion

import "fmt"

// TTSParams 情绪调节后的 TTS 参数
type TTSParams struct {
	Rate   string // 语速，如 "+15%"
	Pitch  string // 音调，如 "+30Hz"
	Volume string // 音量，如 "+10%"
}

// Profile 情绪对应的语音修正
type Profile struct {
	Rate   float64 // 百分比，如 15 表示 +15%
	Pitch  float64 // Hz，如 30 表示 +30Hz
	Volume float64 // 百分比，如 10 表示 +10%
}

var profiles = map[string]Profile{
	"joy":      {Rate: 15, Pitch: 30, Volume: 10},
	"surprise": {Rate: 20, Pitch: 50, Volume: 15},
	"smirk":    {Rate: -10, Pitch: 20, Volume: -15},
	"sadness":  {Rate: -20, Pitch: -30, Volume: -10},
	"anger":    {Rate: 10, Pitch: -10, Volume: 20},
	"fear":     {Rate: 25, Pitch: 40, Volume: -5},
	"neutral":  {Rate: 0, Pitch: 0, Volume: 0},
}

// Modulate 根据情绪和强度计算 TTS 参数
// intensity 0.0~1.0 控制修正幅度
func Modulate(emo string, intensity float64) TTSParams {
	p, ok := profiles[emo]
	if !ok {
		p = profiles["neutral"]
	}
	if intensity < 0 {
		intensity = 0
	}
	if intensity > 1 {
		intensity = 1
	}

	return TTSParams{
		Rate:   fmt.Sprintf("%+.0f%%", p.Rate*intensity),
		Pitch:  fmt.Sprintf("%+.0fHz", p.Pitch*intensity),
		Volume: fmt.Sprintf("%+.0f%%", p.Volume*intensity),
	}
}
