package cas

import (
	"html"
	"regexp"
	"strings"
)

// HTML 解析：CAS 登录页有多个 <form>（userNameLogin / dynamicLogin /
// fidoLogin / qrLogin 等），同名字段（execution、lt 等）在不同 form 间重复，
// 必须按包含 cllt=userNameLogin 的 form 划定作用域再提取 hidden 字段。
// 零依赖约束下用正则切分 form 边界 + 属性解析，替代 Python 侧的 BeautifulSoup。

var (
	formRE   = regexp.MustCompile(`(?is)<form\b[^>]*>.*?</form>`)
	inputRE  = regexp.MustCompile(`(?is)<input\b[^>]*>`)
	attrRE   = regexp.MustCompile(`(?is)([\w-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	tagStrip = regexp.MustCompile(`(?s)<[^>]*>`)
)

// parseAttrs 把一个标签内的属性解析为小写名 → 值 的映射。
func parseAttrs(tag string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range attrRE.FindAllStringSubmatch(tag, -1) {
		v := m[2]
		if m[3] != "" || (m[2] == "" && strings.Contains(m[0], "'")) {
			v = m[3]
		}
		attrs[strings.ToLower(m[1])] = html.UnescapeString(v)
	}
	return attrs
}

// ExtractHiddenFields 提取 <input type=hidden> 的 name(优先)或 id → value。
// cllt 非空时，只在包含 <input name="cllt" value=cllt> 的 <form> 内部抓取。
func ExtractHiddenFields(page, cllt string) map[string]string {
	forms := formRE.FindAllString(page, -1)
	if len(forms) == 0 {
		forms = []string{page}
	}
	if cllt != "" {
		var scoped []string
		for _, f := range forms {
			for _, tag := range inputRE.FindAllString(f, -1) {
				attrs := parseAttrs(tag)
				if attrs["name"] == "cllt" && attrs["value"] == cllt {
					scoped = append(scoped, f)
					break
				}
			}
		}
		forms = scoped
	}
	fields := make(map[string]string)
	for _, f := range forms {
		for _, tag := range inputRE.FindAllString(f, -1) {
			attrs := parseAttrs(tag)
			if !strings.EqualFold(attrs["type"], "hidden") {
				continue
			}
			key := attrs["name"]
			if key == "" {
				key = attrs["id"]
			}
			if key != "" {
				fields[key] = attrs["value"]
			}
		}
	}
	return fields
}

var (
	reauthKeywords  = []string{"reAuthCheck", "Multifactor", "reAuthType", "二次认证"}
	ipFrozenKw      = []string{"IP freeze", "has been blocked", "IP被冻结"}
	errorSelectors  = []string{"showErrorTip", "form-errorTip", "help-block", "reauth_error_submit"}
	errSelectorAttr = buildSelectorREs(errorSelectors)
)

// IsReauthPage 报告页面是否为二次认证（MFA）页。
func IsReauthPage(htmlText string) bool {
	return containsAny(htmlText, reauthKeywords)
}

// IsIPFrozen 报告页面是否为 IP 被冻结提示页。
func IsIPFrozen(htmlText string) bool {
	return containsAny(htmlText, ipFrozenKw)
}

func containsAny(s string, kws []string) bool {
	for _, k := range kws {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// buildSelectorREs 为 id 选择器（#x）与 class 选择器（.x）各生成一个
// 「标签 + 内容」捕获正则。
func buildSelectorREs(selectors []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, sel := range selectors {
		idPat := `(?is)<([a-z0-9]+)\b[^>]*\bid\s*=\s*["']` + regexp.QuoteMeta(sel) + `["'][^>]*>(.*?)</[a-z0-9]+>`
		classPat := `(?is)<([a-z0-9]+)\b[^>]*\bclass\s*=\s*["'][^"']*\b` + regexp.QuoteMeta(sel) + `\b[^"']*["'][^>]*>(.*?)</[a-z0-9]+>`
		out = append(out, regexp.MustCompile(idPat), regexp.MustCompile(classPat))
	}
	return out
}

// ExtractErrorMessage 从登录结果页提取后端错误提示，找不到返回空串。
func ExtractErrorMessage(htmlText string) string {
	for _, re := range errSelectorAttr {
		m := re.FindStringSubmatch(htmlText)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(tagStrip.ReplaceAllString(m[2], ""))
		text = html.UnescapeString(text)
		if text != "" {
			return text
		}
	}
	return ""
}
