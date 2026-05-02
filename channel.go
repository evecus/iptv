package main

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ── Speed tier ────────────────────────────────────────────────────

func speedTier(speed float64) string {
	switch {
	case speed >= SPEED_HIGH:
		return "高速"
	case speed >= SPEED_MID:
		return "普通"
	default:
		return "低速"
	}
}

func tierOrder(tier string) int {
	switch tier {
	case "高速":
		return 0
	case "普通":
		return 1
	default:
		return 2
	}
}

// ── Group / logo ──────────────────────────────────────────────────

func baseGroup(name string) string {
	upper := strings.ToUpper(name)
	if strings.Contains(upper, "CCTV") {
		return "央视"
	}
	if strings.Contains(name, "卫视") {
		return "卫视"
	}
	return "其他"
}

func fullGroup(name string, speed float64) string {
	return baseGroup(name) + "（" + speedTier(speed) + "）"
}

func buildLogoURL(name string) string {
	return LOGO_BASE_URL + url.PathEscape(name) + ".png"
}

func buildM3U8Entry(name, streamURL string, speed float64) string {
	grp := fullGroup(name, speed)
	return fmt.Sprintf(
		"#EXTINF:-1 tvg-name=%q tvg-logo=%q group-title=%q,%s\n%s",
		name, buildLogoURL(name), grp, name, streamURL,
	)
}

// ── Name cleaning ─────────────────────────────────────────────────

var reCCTVNum = regexp.MustCompile(`CCTV(\d+)台`)

var cctvAliases = map[string]string{
	"CCTV1综合": "CCTV1", "CCTV2财经": "CCTV2", "CCTV3综艺": "CCTV3",
	"CCTV4国际": "CCTV4", "CCTV4中文国际": "CCTV4", "CCTV4欧洲": "CCTV4",
	"CCTV5体育": "CCTV5", "CCTV6电影": "CCTV6",
	"CCTV7军事": "CCTV7", "CCTV7军农": "CCTV7", "CCTV7农业": "CCTV7", "CCTV7国防军事": "CCTV7",
	"CCTV8电视剧": "CCTV8", "CCTV9记录": "CCTV9", "CCTV9纪录": "CCTV9",
	"CCTV10科教": "CCTV10", "CCTV11戏曲": "CCTV11", "CCTV12社会与法": "CCTV12",
	"CCTV13新闻": "CCTV13", "CCTV新闻": "CCTV13",
	"CCTV14少儿": "CCTV14", "CCTV15音乐": "CCTV15", "CCTV16奥林匹克": "CCTV16",
	"CCTV17农业农村": "CCTV17", "CCTV17农业": "CCTV17",
	"CCTV5+体育赛视": "CCTV5+", "CCTV5+体育赛事": "CCTV5+", "CCTV5+体育": "CCTV5+",
	"CCTV01": "CCTV1", "CCTV02": "CCTV2", "CCTV03": "CCTV3",
	"CCTV04": "CCTV4", "CCTV05": "CCTV5", "CCTV06": "CCTV6",
	"CCTV07": "CCTV7", "CCTV08": "CCTV8", "CCTV09": "CCTV9",
}

func cleanChannelName(name string) string {
	name = strings.ReplaceAll(name, "cctv", "CCTV")
	name = strings.ReplaceAll(name, "中央", "CCTV")
	name = strings.ReplaceAll(name, "央视", "CCTV")
	for _, rep := range []string{"高清", "超高", "HD", "标清", "频道", "-", " ", "(", ")"} {
		name = strings.ReplaceAll(name, rep, "")
	}
	name = strings.ReplaceAll(name, "PLUS", "+")
	name = strings.ReplaceAll(name, "＋", "+")
	name = reCCTVNum.ReplaceAllString(name, "CCTV$1")
	if mapped, ok := cctvAliases[name]; ok {
		return mapped
	}
	return name
}

// ── Standard name map ─────────────────────────────────────────────

func getStandardChannelMap() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(CHANNEL_LIST_FILE)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		std := strings.TrimSpace(line)
		if std == "" {
			continue
		}
		key := normalKey(std)
		m[key] = std
	}
	return m
}

func normalKey(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(s, "-", ""), " ", ""))
}

func mapToStandardName(name string, m map[string]string) string {
	if std, ok := m[normalKey(name)]; ok {
		return std
	}
	return name
}

// ── Satellite channel order ────────────────────────────────────────

// weixiOrder defines the fixed display order for satellite (卫视) channels.
// Channels not in this list are sorted after all listed channels, alphabetically.
var weixiOrder = []string{
	"湖南卫视", "东方卫视", "浙江卫视", "江苏卫视", "北京卫视",
	"山东卫视", "河南卫视", "广东卫视", "安徽卫视", "深圳卫视",
	"天津卫视", "江西卫视", "四川卫视", "湖北卫视", "重庆卫视",
	"黑龙江卫视", "辽宁卫视", "河北卫视", "吉林卫视", "山西卫视",
	"广西卫视", "云南卫视", "福建东南卫视", "贵州卫视", "陕西卫视",
	"甘肃卫视", "内蒙古卫视", "新疆卫视", "宁夏卫视", "青海卫视",
	"西藏卫视", "海南卫视", "兵团卫视",
}

// weixiSortIndex does a fuzzy lookup: it checks whether the channel name
// *contains* any of the canonical satellite-channel keywords (e.g. "湖南卫视").
// This handles names like "★湖南卫视HD" or "[湖南卫视]" that carry extra
// decoration around the core keyword.
// Returns (index, true) when matched, (-1, false) when not in the list.
func weixiSortIndex(name string) (int, bool) {
	for i, keyword := range weixiOrder {
		if strings.Contains(name, keyword) {
			return i, true
		}
	}
	return -1, false
}

// ── Sort key ──────────────────────────────────────────────────────

func channelSortKey(name string) (int, float64, string) {
	upper := strings.ToUpper(name)
	if strings.Contains(upper, "CCTV") {
		re := regexp.MustCompile(`CCTV(\d+)`)
		if m := re.FindStringSubmatch(upper); m != nil {
			num, _ := strconv.ParseFloat(m[1], 64)
			return 0, num, ""
		}
		if strings.Contains(upper, "5+") {
			return 0, 5.5, ""
		}
		return 0, 999, ""
	}
	if strings.Contains(name, "卫视") {
		if idx, ok := weixiSortIndex(name); ok {
			// Known channel (fuzzy match): sort by fixed index, tiebreak by name
			return 1, float64(idx), name
		}
		// Unknown satellite channel: sort after all known ones, alphabetically
		return 1, float64(len(weixiOrder)), name
	}
	return 2, 0, name
}
