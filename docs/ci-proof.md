# CI proof

PR "Proof: failing test must block merge" contained a deliberately wrong
assertion. The `ci` check failed and GitHub refused the merge because `ci`
is a required status check on main. The PR was closed, not merged.

This file arrived through a normal PR whose `ci` check passed.
