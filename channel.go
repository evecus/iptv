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
		return 1, 0, name
	}
	return 2, 0, name
}
