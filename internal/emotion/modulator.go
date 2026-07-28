package emotion

import "fmt"

// TTSParams 情绪调节后的 TTS 参数
type TTSParams struct {
	Rate   string
	Pitch  string
	Volume string
}

// Profile 情绪对应的语音修正
type Profile struct {
	Rate   float64
	Pitch  float64
	Volume float64
}

// 情绪 → 语音参数（对齐 Mimo TTS 的 speed 配置）
var profiles = map[string]Profile{
	"joy":      {Rate: 25, Pitch: 35, Volume: 15},
	"surprise": {Rate: 30, Pitch: 50, Volume: 20},
	"smirk":    {Rate: 8, Pitch: 15, Volume: -10},
	"sadness":  {Rate: -25, Pitch: -30, Volume: -10},
	"anger":    {Rate: 35, Pitch: -5, Volume: 25},
	"fear":     {Rate: 40, Pitch: 45, Volume: -5},
	"disgust":  {Rate: 15, Pitch: -10, Volume: 10},
	"neutral":  {Rate: 0, Pitch: 0, Volume: 0},
}

// EmotionLabels 所有支持的情绪标签
var EmotionLabels = []string{"joy", "surprise", "smirk", "sadness", "anger", "fear", "disgust", "neutral"}

// Modulate 根据情绪和强度计算 TTS 参数
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
