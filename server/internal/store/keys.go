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
	tagPrefix     = "tag:"
	tagIndexKey   = "tags"
	settingsKey   = "settings"
	auditKey      = "audit"

	// tombstonePrefix marks a device whose group the admin deleted. It is the
	// third kind of key here with a TTL; losing one early only means the
	// device is told it was revoked instead of why.
	tombstonePrefix = "deleted_device:"

	devicesSuffix    = ":devices"
	wrappedKeySuffix = ":wrapped_key"

	// The store (SPEC §4.6). Its records are persistent-zone records like
	// every other key here: no TTL, never evictable.
	storeItemPrefix   = "storeitem:"
	storeIndexSuffix  = ":store"          // group:<id>:store, committed items by saved_at
	storeUsageSuffix  = ":storeusage"     // group:<id>:storeusage, bytes and items held or reserved
	storeLinksSuffix  = ":storelinks"     // group:<id>:storelinks, epoch -> store link
	storeUsageKey     = "store:usage"     // bytes and items across every group
	storePendingKey   = "store:pending"   // items being uploaded, by start time
	storeReclaimKey   = "store:reclaim"   // groups whose store is being emptied
	storeBlobRetryKey = "store:blobretry" // "<group id>/<item id>" whose blob delete failed
)

func tokenKey(value string) string     { return tokenPrefix + value }
func groupKey(id string) string        { return groupPrefix + id }
func groupDevicesKey(id string) string { return groupPrefix + id + devicesSuffix }
func deviceKey(id string) string       { return devicePrefix + id }
func wrappedKeyKey(id string) string   { return devicePrefix + id + wrappedKeySuffix }
func pairingKey(token string) string   { return pairingPrefix + token }
func offerKey(code string) string      { return offerPrefix + code }
func tagKey(name string) string        { return tagPrefix + name }
func tombstoneKey(id string) string    { return tombstonePrefix + id }
func storeUsageKeyOf(id string) string { return groupPrefix + id + storeUsageSuffix }
func storeIndexKey(id string) string   { return groupPrefix + id + storeIndexSuffix }
func storeItemKey(id string) string    { return storeItemPrefix + id }
func storeLinksKey(id string) string   { return groupPrefix + id + storeLinksSuffix }
