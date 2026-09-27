// Starts the documentation demo stack and leaves it running, set up with
// the sample handbook and its people, for exploring or manual captures:
//
//	node e2e/docs/serve.ts
import { PEOPLE } from "./content.ts";
import { URL, startStack } from "./stack.ts";
import { connectHandbook, createAdmin, invitePeople } from "./world.ts";

const stack = await startStack();
for (const sig of ["SIGINT", "SIGTERM"] as const)
  process.on(sig, () => {
    stack.stop();
    process.exit(0);
  });
const maya = await createAdmin();
const { repoID } = await connectHandbook(maya);
await invitePeople(maya, repoID);
console.log(`kmdn: ${URL} (repo ${repoID})`);
console.log(
  `sign in as ${Object.values(PEOPLE)
    .map((p) => p.email)
    .join(", ")}; links are in the log`,
);
