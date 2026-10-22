// The app's own URLs, built in one place so that a group id or a tag name is
// escaped exactly once.

export const groupPath = (id: string) => `/admin/groups/${encodeURIComponent(id)}`;

/** groupsByTag is the groups screen narrowed to one tag, or with "-" to the
 *  untagged groups. */
export const groupsByTag = (filter: string) => `/admin/groups?tag=${encodeURIComponent(filter)}`;

/** qrPath is where a token's QR image is served. Tokens are base64url, so
 *  encoding changes nothing; it guarantees the token stays one path segment. */
export const qrPath = (token: string) => `/admin/tokens/${encodeURIComponent(token)}/qr.png`;
