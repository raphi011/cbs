// ApiError carries the HTTP status so the UI can show a category hint alongside
// the backend's descriptive message. messageForStatus maps the status families
// the backend uses (see api/errors.go) to a short human explanation.

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export function messageForStatus(status: number): string {
  switch (status) {
    case 400:
      return "The request was rejected — check the highlighted fields.";
    case 404:
      return "Not found — state is in-memory and resets when the backend restarts.";
    case 409:
      return "Conflict — this was a duplicate key or has already been applied.";
    case 422:
      return "Not allowed in the current state (insufficient funds, frozen account, or invalid transition).";
    case 502:
      return "The backend is unreachable. Is `go run ./cmd/server` running on :8080?";
    case 500:
      return "The backend hit an unexpected error.";
    default:
      return "Something went wrong.";
  }
}

// describeError returns a one-line summary for toasts/alerts: the category hint
// plus the server's own message when present and different.
export function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    const hint = messageForStatus(err.status);
    if (err.message && err.message !== hint) {
      return `${hint} (${err.message})`;
    }
    return hint;
  }
  if (err instanceof Error) return err.message;
  return "Something went wrong.";
}
