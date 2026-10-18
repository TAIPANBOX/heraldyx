Feature: Mail leaves the box only over an encrypted connection

  @decided 2026-10-17: the mail transport requires encryption by default. The
  only unencrypted path is one the operator names on purpose, and it stays on
  the loopback interface. Every connection has a deadline, so a mail server that
  stops answering cannot hold the notifier.

  Scenario: a remote server that does not offer STARTTLS gets no mail
    Given a mail server that does not offer STARTTLS
    When heraldyx sends an alert with the default TLS setting
    Then the send fails and the server receives nothing
  # @test:TestAPlainRelayThatOffersNoSTARTTLSIsRefused

  Scenario: a server whose certificate is not trusted gets no mail
    Given a mail server that offers STARTTLS with a certificate nobody trusts
    When heraldyx sends an alert with the default TLS setting
    Then the send fails and the server receives nothing
  # @test:TestAStarttlsRelayIsReachedOnlyThroughAVerifiedCertificate

  Scenario: port 465 is spoken with TLS from the first byte
    Given a mail host given with port 465 and no TLS setting
    When the transport is chosen
    Then it uses implicit TLS
  # @test:TestPort465IsImplicitTLSByDefault

  Scenario: an implicit TLS server with a trusted certificate receives the mail
    Given a mail server that speaks TLS from the first byte
    When heraldyx sends an alert in implicit TLS mode
    Then the server receives the message
  # @test:TestAnImplicitTLSRelayReceivesTheMail

  Scenario: plaintext is used only when the operator asks for it on the loopback interface
    Given a relay on 127.0.0.1 that offers no STARTTLS and the TLS setting plain
    When heraldyx sends an alert
    Then the relay receives the message in the clear
  # @test:TestAnExplicitPlainRelayOnLoopbackIsUsed

  Scenario: plaintext toward any other host is refused at startup
    Given the TLS setting plain and a host that is not on the loopback interface
    When the transport is built
    Then it refuses, and the refusal names the setting
  # @test:TestPlaintextToANonLoopbackHostIsRefusedAtStartup

  Scenario: an unknown TLS setting is refused at startup
    Given a TLS setting that is not starttls, implicit or plain
    When the transport is built
    Then it refuses, and the refusal lists the three values
  # @test:TestAnUnknownTLSModeIsRefused

  Scenario: a server that stops answering is cut off at the deadline
    Given a mail server that accepts the connection and then says nothing
    When heraldyx sends an alert
    Then the send fails at the deadline instead of hanging
  # @test:TestAStalledSessionIsCutOffAtTheDeadline

  Scenario: a host that never accepts the connection is given up on at the dial deadline
    Given a mail host that does not accept a connection
    When heraldyx sends an alert
    Then the send fails at the dial deadline
  # @test:TestAHostThatNeverAcceptsIsGivenUpOnAtTheDialDeadline
