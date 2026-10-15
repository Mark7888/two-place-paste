package quota

import "github.com/redis/go-redis/v9"

// NewRedisNamespaced is NewRedis under a test-chosen key namespace, so that
// parallel tests sharing one Redis keep separate totals.
func NewRedisNamespaced(rdb redis.Cmdable, opts Options, namespace string) *Redis {
	return newRedis(rdb, opts, namespace)
}
