import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// RTL doesn't auto-cleanup under Vitest's default `globals: true` mode
// (no afterEach hook from @testing-library/react), so wire it up here
// to keep tests isolated.
afterEach(() => cleanup());
