package logger

import shared "github.com/CarlSinclair/csuit-libs/logger"

const AppName = "tvt"

func init() {
	shared.Setup(shared.Options{
		AppName:            AppName,
		DefaultServiceName: "tvt",
	})
}
