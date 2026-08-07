import { redirect } from "next/navigation";

// The back office lives at /bank/[pid]/…. Links and bookmarks pointing at the
// older /participants/[pid]/… land here and are forwarded rather than 404ing. A
// catch-all needs at least one segment, which is exactly right:
// /participants alone was never a page.
export default async function ParticipantsRedirect({
  params,
}: {
  params: Promise<{ rest: string[] }>;
}) {
  const { rest } = await params;
  redirect(`/bank/${rest.join("/")}`);
}
