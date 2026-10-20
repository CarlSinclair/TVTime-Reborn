package handlers

var loveRoutes = map[string]string{"pt": "/pt-br", "es": "/es", "ar": "/ar"}

func loveHref(lang string) string {
	route := loveRoutes[lang]
	if route == "" {
		route = "/"
	}
	return "https://carl-sinclair.com" + route + "?ref=tvt"
}
