package agentops

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

//go:embed content_rules.md
var contentRules string

var (
	numberedList   = regexp.MustCompile(`[0-9]️⃣|[①②③④⑤⑥⑦⑧⑨]|(?m)^\s*[0-9]+[.、．]`)
	orderWords     = regexp.MustCompile(`首先.*其次|第一[，、：:].*第二`)
	templatePhrase = regexp.MustCompile(`总的来说|总而言之|综上|希望(对你|能)?(对大家)?有(所)?帮助|一起加油|快来试试|赋能|全方位|干货满满|不容错过|值得一提|不仅.{1,20}还|在.{1,12}的今天|让我们一起`)
	selfLabel      = regexp.MustCompile(`AI ?(协助|生成|辅助|撰写)|由程序生成|程序画的`)
	closingAsk     = regexp.MustCompile(`(你|大家|你们)[^。！？\n]{0,20}[？?]\s*(#[^\n]*)?\s*$`)
)

// StyleCheck lists what makes a draft read as machine-written. These are editorial
// judgments, not proven effects: tools/content-lab/validate.py has not confirmed
// them, so they only warn, and docs/CONTENT_PLAYBOOK.md tracks each one's status.
func StyleCheck(kind, body string) []string {
	out := []string{}
	if len(numberedList.FindAllString(body, -1)) >= 2 {
		out = append(out, "用了编号列表，读起来像模板（效果未验证）")
	}
	if orderWords.MatchString(strings.ReplaceAll(body, "\n", " ")) {
		out = append(out, "用了“首先其次/第一第二”的排比")
	}
	if m := templatePhrase.FindString(body); m != "" {
		out = append(out, fmt.Sprintf("出现模板用语“%s”", m))
	}
	if m := selfLabel.FindString(body); m != "" {
		out = append(out, fmt.Sprintf("出现了你要求去掉的声明“%s”", m))
	}
	if closingAsk.MatchString(strings.TrimSpace(body)) {
		out = append(out, "靠结尾提问换评论，读起来像模板（效果未验证）")
	}
	if kind == "note" {
		text := strings.Join(strings.Fields(body), "")
		if n := utf8.RuneCountInString(text); n > 300 && !numberedList.MatchString(body) {
			out = append(out, fmt.Sprintf("正文 %d 字偏长，想想能不能只讲一个点", n))
		}
		paragraphs := []int{}
		for _, p := range strings.Split(body, "\n\n") {
			if p = strings.TrimSpace(p); p != "" && !strings.HasPrefix(p, "#") {
				paragraphs = append(paragraphs, utf8.RuneCountInString(p))
			}
		}
		if len(paragraphs) >= 4 && evenLengths(paragraphs) {
			out = append(out, "各段长度过于整齐")
		}
	}
	return out
}

// evenLengths reports whether every paragraph is within 40% of the average.
func evenLengths(lengths []int) bool {
	total := 0
	for _, n := range lengths {
		total += n
	}
	avg := float64(total) / float64(len(lengths))
	for _, n := range lengths {
		if d := float64(n) - avg; d > avg*0.4 || d < -avg*0.4 {
			return false
		}
	}
	return true
}
