import { redirect } from "next/navigation";

// Interim root. The lobby lands here in the next task; until then the root
// forwards to the network overview rather than 404ing.
export default function RootPage() {
  redirect("/clearing-house");
}
