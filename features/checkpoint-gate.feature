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

  Scenario: a stream and its checkpoints written by a real producer are accepted as they are
    Given a producer that writes its stream with agent-stack-go's own checkpointing writer and signs with an ES256 key pinned here
    When heraldyx polls the stream with the gate on
    Then every line is mailed and nothing is refused
  # @test:TestARealProducersCheckpointsAreAcceptedAsTheyAre

  Scenario: one byte changed in a line a real producer's checkpoint covers
    Given that stream with one byte of its second line changed after it was checkpointed
    When heraldyx polls it with the gate on
    Then no line of it is mailed, and one mail names the stream and says its bytes no longer match
  # @test:TestAOneByteRewriteOfARealProducersStreamIsRefused

  Scenario: a real producer's stream cut short of its checkpoint
    Given that stream cut to four of its seven lines
    When heraldyx polls it with the gate on
    Then no line of it is mailed, and one mail names the stream and says it is shorter than its newest checkpoint
  # @test:TestATruncatedRealProducersStreamIsRefused

  Scenario: a producer's renewed keys keep its stream covered through custody
    Given the gate in the custody mode, and a producer whose checkpoint keys custody certifies and publishes, renewed partway through its stream
    When heraldyx polls the stream before and after the resolver reads each key's file
    Then the stream is covered once custody certified the first key, and stays covered through the renewal
  # @test:TestAProducersRenewedKeysKeepItsStreamCoveredThroughCustody

  Scenario: a key revoked at custody uncovers its stream
    Given a stream covered by a key custody certified
    When custody publishes the key's revocation and the resolver reads it
    Then the stream is no longer covered, and the reason says its key is no longer certified
  # @test:TestARevokedKeyUncoversItsStream

  Scenario: a root half custody stops publishing uncovers its stream
    Given a stream covered by a key custody certified
    When custody's published root key set no longer carries the pinned kids
    Then the stream is no longer covered, and the reason says its key is no longer certified
  # @test:TestARootHalfCustodyStopsPublishingUncoversTheStream

  Scenario: another producer's valid key does not cover this stream
    Given checkpoints signed by a key custody certified for another producer's key name
    When heraldyx polls this stream
    Then no signed checkpoint covers it
  # @test:TestAKeyCertifiedForAnotherProducerDoesNotCover

  Scenario: the custody mode is read whole or refused
    Given the gate on with custody's statements directory, and in turn the pinned file as well, neither, no pin, half a pin, no binding, or a directory that is relative, missing or this process's own
    When heraldyx reads its configuration
    Then the whole configuration is read and each broken one refuses the start
  # @test:TestTheCustodyModeIsReadWholeOrRefused


  Scenario: a held file holds only itself, and only from its event
    Given one file the gate has not decided, with a malformed line before its event, and another file it covers
    When the bus is read, and again once the first file is decided
    Then the other file is read at once, and the held event is read later without its malformed line counted twice
  # @test:TestAHeldFileHoldsOnlyItselfAndOnlyFromItsEvent

  Scenario: held reads are said at most once a minute
    Given reads held on every poll for a while
    When the counts are reported
    Then a line goes out when the count has grown and a minute has passed since the last, saying what was added and the total
  # @test:TestHeldReadsAreSaidAtMostOnceAMinute
