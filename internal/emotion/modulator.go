package emotion

import (
	"fmt"
	"strconv"
	"strings"
)

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
	"joy":      {Rate: 6, Pitch: 8, Volume: 3},
	"surprise": {Rate: 7, Pitch: 10, Volume: 4},
	"smirk":    {Rate: 3, Pitch: 5, Volume: 0},
	"sadness":  {Rate: -6, Pitch: -6, Volume: -3},
	"anger":    {Rate: 5, Pitch: -2, Volume: 4},
	"fear":     {Rate: 4, Pitch: 6, Volume: -2},
	"disgust":  {Rate: -2, Pitch: -3, Volume: 0},
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

// BoostRate 在现有 Rate 百分比上叠加 boost（积压加速），返回新参数。
// 例如 Rate "+25%"、boost 0.15 → "+40%"；解析失败时原样返回。
func BoostRate(p TTSParams, boost float64) TTSParams {
	if boost <= 0 {
		return p
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(p.Rate, "+"), "%")
	v, err := strconv.Atoi(raw)
	if err != nil {
		return p
	}
	p.Rate = fmt.Sprintf("%+.0f%%", float64(v)+boost*100)
	return p
}
