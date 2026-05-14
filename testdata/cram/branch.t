Branching from an old version.

Create a base version and then a linear child version.

  $ write branch.txt
  > a
  > b
  $ sfvc init branch.txt -m base
  initialized branch.txt at version 1
  $ write branch.txt
  > a
  > b2
  $ sfvc commit branch.txt -m "v2 change"
  committed version 2

Checking out v1 before committing should create a sibling of v2, not a child of the previous head.

  $ sfvc checkout branch.txt 1
  checked out version 1
  $ write branch.txt
  > a
  > c
  $ sfvc commit branch.txt -m "branch from v1"
  committed version 3

Each version should reconstruct its own contents.

  $ sfvc show branch.txt 1
  a
  b
  $ sfvc show branch.txt 2
  a
  b2
  $ sfvc show branch.txt 3
  a
  c
  $ sfvc show branch.txt
  a
  c

The log command follows only the current head ancestry, so sibling v2 is not shown here.

  $ sfvc log branch.txt
  @  v3   tester     2001-02-03 04:05:06 +0000
  │  branch from v1
  │
  *  v1   tester     2001-02-03 04:05:06 +0000
  │  base
  │
  ~

TODO: display branches correctly.
The --all view shows the complete flat history so the user can discover other versions to check out.

  $ sfvc log --all branch.txt
  @  v3   tester     2001-02-03 04:05:06 +0000
  │  branch from v1
  │
  *  v2   tester     2001-02-03 04:05:06 +0000
  │  v2 change
  │
  *  v1   tester     2001-02-03 04:05:06 +0000
  │  base
  │
  ~
