Basic single-file workflow.

Create a small tracked file and initialize the weave.

  $ write note.txt
  > one
  > two
  $ sfvc init note.txt -m first
  initialized note.txt at version 1
  $ test -f .sfvc/note.txt.weave

Change the working file and verify that diff compares it against the current head version.

  $ write note.txt
  > one
  > three
  $ sfvc diff note.txt
  --- note.txt
  +++ note.txt
  @@ -1,2 +1,2 @@
   one
  -two
  +three

Commit the change.
Committing again without edits should be a no-op.

  $ sfvc commit note.txt -m "replace two\nbody ignored"
  committed version 2
  $ sfvc commit note.txt -m noop
  no changes

The show command can reconstruct an explicit version and the current head.

  $ sfvc show note.txt 1
  one
  two
  $ sfvc show note.txt
  one
  three

The log command walks backward from the head.

  $ sfvc log note.txt
  @  v2   tester     2001-02-03 04:05:06 +0000
  │  replace two
  │
  *  v1   tester     2001-02-03 04:05:06 +0000
  │  first
  │
  ~

Checkout rewrites the working file and moves head to the requested version.

  $ sfvc checkout note.txt 1
  checked out version 1
  $ cat note.txt
  one
  two
