package main

import (
	"tvt/config"
	"tvt/logger"

	"github.com/CarlSinclair/csuit-libs/redis"
	"github.com/CarlSinclair/csuit-libs/redis/embedded"
)

const redisPrefix = "tvt:"

// startRedis points the shared packages that are written against Redis at the
// server set in Admin Config (or TVT_REDIS_ADDR). Without one, TVT runs a small
// in-memory server of its own so those features still work; its state does
// not survive a restart. Redis is optional here, so a failure is logged and
// the features that need it report themselves unavailable.
func startRedis(cfg *config.Config) func() {
	if cfg.RedisAddr != "" {
		redis.Configure(redis.Options{
			DefaultPrefix: redisPrefix,
			Settings: func() (redis.Settings, error) {
				return redis.Settings{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB, Prefix: redisPrefix}, nil
			},
		})
		redis.Init("")
		return func() { redis.Close() }
	}
	opts, stop, err := embedded.Start(redisPrefix)
	if err != nil {
		logger.Error("redis: the built-in server could not start, sign-in with Telegram and other Redis features are unavailable: %v", err)
		return func() {}
	}
	redis.Configure(opts)
	redis.Init("")
	return func() {
		redis.Close()
		stop()
	}
}
