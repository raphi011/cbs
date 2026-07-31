// Each entity has a listener of its own — one per member bank, one for the
// central bank, one for the clearing house — so a request has to say which one
// it is for. The first segment after /api is the operator key; the proxy strips
// it and forwards the rest to that listener.
//
// The port table lives in the proxy, not here. A base URL in a DTO would make
// the member roster a deployment manifest, and where a bank happens to be bound
// is not domain data.

/** The settlement layer: reserves, its own audit, admission, settling a cycle. */
export const cb = (path: string) => `/central-bank${path}`;

/** The CSM: every payment, the cycles, the schemes, the mandates, the directory. */
export const csm = (path: string) => `/clearing-house${path}`;

/**
 * One member bank's own surface. The pid is the operator key rather than a path
 * segment the bank has to be told — on the wire it selects the listener, and the
 * listener has already answered "which bank?".
 */
export const bank = (pid: string, path: string) => `/bank/${pid}${path}`;
