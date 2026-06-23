// Idempotency keys make POST /transactions safe to retry: the backend rejects a
// second transaction with the same key (409), so a network retry can't
// double-post. We generate a fresh UUID per attempt.

export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}
