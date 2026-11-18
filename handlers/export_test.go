package handlers

func ResetCSPForTest() { invalidateCSP() }

func SetCaptchaVerifyURLForTest(provider, verifyURL string) (restore func()) {
	spec := captchaSpecs[provider]
	old := spec.verify
	spec.verify = verifyURL
	captchaSpecs[provider] = spec
	return func() {
		spec.verify = old
		captchaSpecs[provider] = spec
	}
}
