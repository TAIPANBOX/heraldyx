Feature: a stream with no signed checkpoint is not mailed, when the operator switches the gate on

  F-018 of the 2026-10-17 security review: a bus consumer cannot tell who wrote a
  line, because any writer can append to any file on the shared bus. With
  HERALDYX_CHECKPOINT_GATE=on, heraldyx reads a stream only when a signed
  checkpoint (agent-passport SPEC.md 6.5.1, format v1) covers it, and says in a
  mail what it refused and why. The gate is OFF by default, so a box that does
  not switch it on reads exactly as it did (@decided 2026-10-17 for the wave;
  the switch and its default are @claude 2026-10-18). Coverage is checked for
  the whole stream when a new checkpoint arrives, not line by line; a line after
  the newest checkpoint is covered by the stream's coverage, and the witness is
  what checks those bytes later.

  Scenario: the gate is off unless the operator switches it on
    Given an environment with no HERALDYX_CHECKPOINT_GATE set
    When the configuration is read
    Then the gate is off and no checkpoint file is required
  # @test:TestTheCheckpointGateIsOffUnlessSwitchedOn

  Scenario: with the gate off, a stream without a checkpoint is read as it was before
    Given wardryx.ndjson with a policy_deny line and no checkpoint file
    When the box polls with the gate off
    Then the line is mailed, and nothing is said about checkpoints
  # @test:TestGateOffLeavesAnUncheckedStreamAsItWas

  Scenario: a stream with a valid signed checkpoint is read
    Given wardryx.ndjson, and a checkpoint signed by a pinned key whose count, prefix and head match the stream
    When the checkpoint is checked
    Then the stream is covered
  # @test:TestAValidSignedCheckpointCoversItsStream

  Scenario: a stream with no checkpoint is refused and the refusal is said in a mail
    Given wardryx.ndjson with a policy_deny line, the gate on, and no checkpoint file
    When the box polls
    Then the line is not mailed, and a mail says that wardryx.ndjson has no signed checkpoint and how many events were refused
  # @test:TestAStreamWithNoCheckpointIsRefusedAndSaidInAMail

  Scenario: a checkpoint signed by a key nobody pinned does not cover the stream
    Given a checkpoint whose signature is valid for a key that is not in the keys file
    When the checkpoint is checked
    Then the stream is not covered
  # @test:TestACheckpointFromAnUnpinnedKeyDoesNotCover

  Scenario: a forged signature does not cover the stream
    Given a checkpoint whose kid is pinned and whose signature bytes are altered
    When the checkpoint is checked
    Then the stream is not covered
  # @test:TestAForgedSignatureDoesNotCover

  Scenario: a stream rewritten after its checkpoint is not covered by it
    Given a checkpoint signed over the original first line, and wardryx.ndjson whose first line was changed by one character
    When the checkpoint is checked
    Then the stream is not covered, because the bytes the checkpoint signed are gone
  # @test:TestARewrittenStreamIsNotCoveredByItsCheckpoint

  Scenario: a checkpoint for another stream does not cover this one
    Given a checkpoint naming tokenfuse.ndjson placed at wardryx.ndjson's checkpoint path
    When the checkpoint is checked
    Then the stream is not covered
  # @test:TestACheckpointForAnotherStreamDoesNotCover

  Scenario: a stream shorter than its checkpoint is not covered
    Given a checkpoint that counts three lines and a stream that holds two
    When the checkpoint is checked
    Then the stream is not covered and the reason says the stream is shorter
  # @test:TestAStreamShorterThanItsCheckpointIsNotCovered

  Scenario: a torn final line in the checkpoint file is ignored
    Given a valid checkpoint followed by a line with no LF at the end
    When the checkpoint is checked
    Then the stream is covered, and the torn line is not reported as invalid
  # @test:TestATornFinalCheckpointLineIsIgnored

  Scenario: a checkpoint file with a line outside format v1 refuses the whole file
    Given a valid checkpoint followed by a line with an extra member
    When the checkpoint is checked
    Then the stream is not covered, because a verifier refuses the file rather than guess at it
  # @test:TestAnAlienCheckpointLineRefusesTheFile

  Scenario: a broken link in the checkpoint chain refuses the file
    Given two signed checkpoints whose second one names the wrong previous record
    When the checkpoint is checked
    Then the stream is not covered
  # @test:TestACheckpointChainBreakRefusesTheFile

  Scenario: coverage is checked again only when a new checkpoint arrives
    Given a covered stream whose checkpoint file has not changed since the last check
    When the stream is checked again
    Then the verdict is reused and the stream is not hashed again
  # @test:TestCoverageIsRecomputedOnlyWhenTheCheckpointChanges

  Scenario: the gate on with no usable keys refuses to start
    Given HERALDYX_CHECKPOINT_GATE=on and a keys file that holds no key
    When the configuration is loaded
    Then loading the keys fails, and the process does not start
  # @test:TestAGateWithNoUsableKeysRefusesStartup

  Scenario: the gate on with no checkpoint directory refuses to start
    Given HERALDYX_CHECKPOINT_GATE=on and no HERALDYX_CHECKPOINTS
    When the configuration is read
    Then the configuration fails, naming the missing directory
  # @test:TestAGateWithNoCheckpointDirRefusesStartup

  Scenario: a refusal is raised once per file and counted on every poll
    Given a stream refused by the gate across three polls
    When the mail is checked
    Then one refusal mail goes for that file and the refused lines are counted each poll
  # @test:TestARefusalIsRaisedOncePerFile
