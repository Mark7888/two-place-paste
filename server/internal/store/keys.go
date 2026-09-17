package store

// Redis key layout for the persistent zone (SPEC §4.2).
//
// Every key below is written WITHOUT a TTL except pairings and pairing offers,
// which is what makes `maxmemory-policy volatile-lru` correct and
// `allkeys-lru` destructive:
// under allkeys-lru Redis is free to evict a group or a device record, and the
// group is then unrecoverable.
const (
	tokenPrefix   = "token:"
	tokenIndexKey = "tokens"
	groupPrefix   = "group:"
	devicePrefix  = "device:"
	pairingPrefix = "pairing:"
	offerPrefix   = "offer:"

	devicesSuffix    = ":devices"
	wrappedKeySuffix = ":wrapped_key"
)

func tokenKey(value string) string     { return tokenPrefix + value }
func groupKey(id string) string        { return groupPrefix + id }
func groupDevicesKey(id string) string { return groupPrefix + id + devicesSuffix }
func deviceKey(id string) string       { return devicePrefix + id }
func wrappedKeyKey(id string) string   { return devicePrefix + id + wrappedKeySuffix }
func pairingKey(token string) string   { return pairingPrefix + token }
func offerKey(code string) string      { return offerPrefix + code }
