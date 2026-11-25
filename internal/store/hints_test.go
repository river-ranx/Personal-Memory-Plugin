package store

import "testing"

func codes(hs []Hint) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Code)
	}
	return out
}

// A spec sheet and a status are noticed; a lesson, a date and a version are not.
func TestWriteHintsNoticeSpecSheetsAndStatusesNotLessons(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       []string
	}{
		{"spec sheet from the audit", "控件 28 高、12.5 中粗、右缘 14、控件同宽 150/170、行分隔线内缩 12、说明行同高 32", []string{"manyNumbers"}},
		{"measurements of one build", "Debug 实测展开起动延迟 470–540ms，其它格式 70–200ms，Release 432→255ms", []string{"manyNumbers"}},
		{"units and words glued to numbers", "按钮28高、字号12px、间距8pt、圆角6dp、右缘14、行距20", []string{"manyNumbers"}},
		{"status", "设计稿已改，Swift 仍是旧的，待用户确认后再实现", []string{"status"}},
		{"both", "Swift 未改，待用户确认；弹窗 664、520、560、500、28、30 都对齐", []string{"manyNumbers", "status"}},
		{"a lesson", "城镇化率一律用常住口径，因为 2018 年以前的户籍口径和之后不可比", nil},
		{"dates and versions", "2026-11-20 起发版走 v1.2.3 之后的流程，旧的 v0.9.1 不再维护，见 2026-10-01 的决定", nil},
		{"a word with a digit", "用 utf8 和 k8s 的时候别忘了 sha256 校验和 md5 都不要", nil},
		{"a bare 已改 is how things are", "签名已改由 CI 完成，本机不再有签名证书", nil},
		{"now is not a status", "目前这台机器的 8765 端口被应用占着，现在查要用 lsof", nil},
	} {
		if got := codes(WriteHints(c.text)); len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) || (len(got) > 1 && got[1] != c.want[1]) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
