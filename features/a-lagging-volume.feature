Feature: A shared volume that lags its writer costs the operator neither a second copy of mail nor an event

  On Kubernetes the event log sits on a Longhorn RWX volume, which is NFS.
  On a GCP cluster on 2026-10-14 a reader saw stale file sizes for about two
  minutes. This process used to take any size smaller than its read position
  as a truncation and read the whole file again, mailing old alerts a second
  time, and to count a line of zero bytes (a span a writer's bytes have not
  reached yet) as malformed and skip it for good. The asks, from that run's
  findings: start a file again only when it is shown to be a different file,
  and otherwise wait for it to catch up; and stop before a line with a zero
  byte in it, look again for a bounded time, and then say so rather than skip
  it in silence.

  Scenario: a file that looks shorter but is the same file is not read again
    Given two events were read from a file
    When the file this process can see is cut back to its first line, with the same first line
    Then nothing is read again, nothing is counted as a truncation, and the read position stays where it was
  # @test:TestAFileThatLooksShorterButIsTheSameFileIsNotReadAgain

  Scenario: reading resumes where it stopped when the file catches up
    Given a file has looked shorter than what was read from it
    When its bytes arrive again with one new event after them
    Then only the new event is read
  # @test:TestReadingResumesWhereItStoppedWhenTheFileCatchesUp

  Scenario: a stale size with the data already there is read through at once
    Given the size this process is told is anywhere from zero to one byte short of what it read
    When a new event has been written after the read position
    Then the new event is read on that poll and nothing is read twice
  # @test:TestAStaleSizeWithTheDataThereIsReadThroughAtOnce

  Scenario: a quiet file whose size stays stale is never read again
    Given a file with nothing new in it whose size stays stale for twenty minutes
    When it is polled every minute
    Then nothing is read again and nothing is given up on
  # @test:TestAQuietFileWithAStaleSizeIsNeverReadAgain

  Scenario: a different file is read from its start, whatever its size
    Given a file was read and then replaced by a file with a different first line
    When the replacement is shorter, already longer, or appears after the file was emptied
    Then the replacement is read from its first event, and that is counted once as a truncation
  # @test:TestAFileReplacedByADifferentOneIsReadFromItsStart

  Scenario: a file that stays shorter for the whole window is read again from its start
    Given a file has looked shorter than what was read from it
    When five minutes pass and it has not caught up
    Then it is read again from its start, a second before that it was not, and it is counted so the log can say so
  # @test:TestAFileThatStaysShorterPastTheWindowIsReadAgainFromTheStart

  Scenario: a restart onto a lagging volume mails nothing twice
    Given two alerts were mailed and the process restarts where the event log looks shorter
    When it polls, and later the log catches up with one new alert
    Then nothing is mailed on the first poll, only the new alert on the later one, and the log says the file looked shorter
  # @test:TestARestartOnALaggingVolumeDoesNotMailOldAlertsAgain

  Scenario: a restart remembers which file it was reading
    Given the state file was saved after reading an event log
    When the log is replaced by a different file while the process is down
    Then the state file names the first line it had read, and the replacement's alert is mailed on the first poll
  # @test:TestARestartRemembersWhichFileItWasReading

  Scenario: a line with a zero byte in it is held until it fills in
    Given zero bytes inside a line, across a newline, over a whole line, or over the first line
    When the file is polled, and polled again before the span fills in
    Then the events before the span are read and nothing after it, and once it fills in every event in it arrives whole
  # @test:TestALineWithAZeroByteIsHeldUntilItFillsIn

  Scenario: a line still zero after the window is skipped and counted
    Given a span of zero bytes that never fills in
    When five minutes pass
    Then the line is skipped, counted, and the events after it arrive, a second before that they did not
  # @test:TestAZeroLineStillThereAfterTheWindowIsSkippedAndCounted

  Scenario: each held line gets its own window
    Given a second span of zero bytes is met after a first one filled in three minutes later
    When six minutes have passed since the first hold
    Then the second line is still held, and it is skipped five minutes after its own hold began
  # @test:TestEachHeldLineGetsItsOwnWindow

  Scenario: what is held, given up on, or started again is said once per growth
    Given the counters for a file that looked shorter, one given up on, a different file, held lines and skipped lines
    When they grow over several polls
    Then each growth is said in one log line, and a poll with no growth says nothing
  # @test:TestHoldsAndSkipsAreSaidOncePerGrowth
