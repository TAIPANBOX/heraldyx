Feature: Failed calls do not imply refunds
  @measured go test ./internal/render -run TestAFailedCallDoesNotPromiseARefund 2026-09-19:
  send failures and buffered body errors were described as free with all reserves released.

  # @test:TestAFailedCallDoesNotPromiseARefund
  Scenario: A send failure does not establish provider execution
    Given a failed send or an unrecognized failure stage
    When its notification is rendered
    Then it reports the error and directs the operator to charge and reservation state without promising a refund

  # @test:TestABufferedBodyFailureNamesThePossibleCharge
  Scenario: A successful response breaks during collection
    Given a provider response body that fails
    When its notification is rendered
    Then it explains the reported-usage or estimate charge and the refusal exception
