package cas

import "testing"

// 模拟 CAS 登录页：多个 <form>，execution/pwdEncryptSalt 只在
// userNameLogin 表单里；dynamicLogin 表单有同名 execution 干扰字段。
const loginPageFixture = `<!DOCTYPE html>
<html><body>
<form id="fm1" action="/authserver/login" method="post">
  <input type="hidden" name="execution" value="e1s1_EXECUTION_userNameLogin"/>
  <input type="hidden" name="_eventId" value="submit"/>
  <input type="hidden" name="cllt" value="userNameLogin"/>
  <input type="hidden" name="dllt" value="generalLogin"/>
  <input type="hidden" name="lt" value=""/>
  <input type="hidden" id="pwdEncryptSalt" value="Ab3dFghJkmNprstX"/>
  <input type="text" name="username"/>
</form>
<form id="fm2" action="/authserver/login" method="post">
  <input type="hidden" name="execution" value="WRONG_dynamicLogin"/>
  <input type="hidden" name="cllt" value="dynamicLogin"/>
</form>
</body></html>`

func TestExtractHiddenFieldsScoped(t *testing.T) {
	fields := ExtractHiddenFields(loginPageFixture, "userNameLogin")
	if got := fields["execution"]; got != "e1s1_EXECUTION_userNameLogin" {
		t.Errorf("execution = %q, want userNameLogin form value", got)
	}
	// pwdEncryptSalt 只有 id 没有 name，应以 id 为键
	if got := fields["pwdEncryptSalt"]; got != "Ab3dFghJkmNprstX" {
		t.Errorf("pwdEncryptSalt = %q", got)
	}
	if _, ok := fields["username"]; ok {
		t.Error("non-hidden input should not be collected")
	}
}

func TestExtractHiddenFieldsUnscoped(t *testing.T) {
	fields := ExtractHiddenFields(loginPageFixture, "")
	// 未限定 form 时后出现的 form 覆盖同名字段（与 Python 侧行为一致：
	// 后者 dict 赋值覆盖）；这里只验证能取到值
	if fields["execution"] == "" {
		t.Error("execution should be present")
	}
}

func TestExtractErrorMessage(t *testing.T) {
	html := `<div id="showErrorTip"><span>  用户名或密码错误  </span></div>`
	if got := ExtractErrorMessage(html); got != "用户名或密码错误" {
		t.Errorf("got %q", got)
	}
	if got := ExtractErrorMessage(`<div>ok</div>`); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestPageClassifiers(t *testing.T) {
	if !IsReauthPage(`<a href="/authserver/reAuthCheck/initReAuth.do">`) {
		t.Error("reauth page not detected")
	}
	if !IsIPFrozen(`<p>IP被冻结，请稍后再试</p>`) {
		t.Error("ip frozen page not detected")
	}
	if IsReauthPage(loginPageFixture) || IsIPFrozen(loginPageFixture) {
		t.Error("login page misclassified")
	}
}
