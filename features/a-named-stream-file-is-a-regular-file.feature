Feature: an entry that names one stream file is read only while it is a regular file

  HERALDYX_EVENTS takes directories of stream files and single stream files.
  A single file is often on a volume another process writes, the Keyryx
  witness's findings for one. If a link or a directory could stand at that
  name, whoever writes that volume could make this process read a stream it
  was never told to read, under the stem of the one it was. So an entry that
  ends in .ndjson is checked with lstat: it is read only while it is a
  regular file, never followed as a link and never expanded as a directory,
  and the startup line names every such entry it does not read
  (@decided 2026-10-20: readers get read-only access to the witness's own
  findings file and the witness never writes the bus; the regular-file rule
  is @claude 2026-10-20, from the security review of that change).

  Scenario: a link named as a stream file is not read
    Given HERALDYX_EVENTS names keyryx.ndjson, and a link stands there pointing at wardryx.ndjson on the bus
    When heraldyx resolves what to read
    Then it reads nothing from that entry, and says the entry is a link that is never followed
  # @test:TestANamedStreamFileThatIsASymlinkIsNotRead

  Scenario: a directory named as a stream file is not expanded
    Given HERALDYX_EVENTS names keyryx.ndjson, and a directory holding wardryx.ndjson stands there
    When heraldyx resolves what to read
    Then it reads nothing from that entry, and says the entry is a directory that is never expanded
  # @test:TestANamedStreamFileThatIsADirectoryIsNotExpanded

  Scenario: a regular stream file is read beside a directory of them
    Given HERALDYX_EVENTS names the bus directory, a regular keyryx.ndjson, and a stream file not there yet
    When heraldyx resolves what to read
    Then it reads the bus's files and keyryx.ndjson, and names nothing as unread
  # @test:TestANamedRegularStreamFileIsReadBesideADirectory

  Scenario: a stream file entry that cannot be checked is not read, and the log says why
    Given HERALDYX_EVENTS names tokenfuse.ndjson inside a directory nobody may enter
    When heraldyx starts
    Then it does not read the entry, keeps running, and its log names the lstat error
  # @test:TestWhatGoesWrongAroundItIsSaidAndItKeepsGoing
