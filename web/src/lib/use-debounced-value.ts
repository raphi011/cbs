"use client";

import { useEffect, useState } from "react";

// Settles a value that changes per keystroke. Both directory screens resolve a
// typed address as you type; without this they would fire a request per
// character, and each miss along the way would be cached under its own key.
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), delayMs);
    return () => clearTimeout(t);
  }, [value, delayMs]);
  return settled;
}
