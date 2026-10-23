// The app's own URLs, built in one place so that a group id or a tag name is
// escaped exactly once.

/** groupPath is a group's page, named by its handle. */
export const groupPath = (handle: string) => `/admin/groups/${encodeURIComponent(handle)}`;

/** groupsByTag is the groups screen narrowed to one tag, or with "-" to the
 *  untagged groups. */
export const groupsByTag = (filter: string) => `/admin/groups?tag=${encodeURIComponent(filter)}`;

/** qrPath is where a token's QR image is served, by the token's handle.
 *  Handles are base64url, so encoding changes nothing; it guarantees the
 *  handle stays one path segment. */
export const qrPath = (handle: string) => `/admin/tokens/${encodeURIComponent(handle)}/qr.png`;
