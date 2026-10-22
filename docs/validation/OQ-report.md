# Operational Qualification report — Open Spoor

- Generated: 2026-10-22T23:12:23Z
- Commit: `71b4fcd`
- Platform: linux/amd64, 4 CPUs, go1.26.0
- Tests: **41 passed, 0 failed, 0 skipped**

## Requirements traceability

| Requirement | Tests | Result |
|---|---|---|
| URS-01 | internal/platform/TestTenantIsolation<br>internal/saas/TestSaaSLifecycle | verified |
| URS-02 | internal/platform/TestPlatformFlow<br>internal/platform/TestAuditImmutable | verified |
| URS-03 | internal/platform/TestPlatformFlow<br>internal/act/TestWorkflowsFormsAndAssistant | verified |
| URS-04 | internal/epcis/TestCaptureTraceLifecycle<br>internal/serialization/TestRandomSerialsUniqueAndReversible<br>internal/migration/TestMigrationAndShadow | verified |
| URS-05 | internal/serialization/TestRandomSerialsUniqueAndReversible<br>internal/serialization/TestDomainMinimum | verified |
| URS-06 | internal/epcis/TestCaptureRejections<br>pkg/epcis/TestValidationErrors<br>internal/verify/TestPlayground | verified |
| URS-07 | internal/epcis/TestCaptureTraceLifecycle | verified |
| URS-08 | internal/dscsa/TestDSCSAManufacturerToWholesaler | verified |
| URS-09 | internal/dscsa/TestDSCSAManufacturerToWholesaler<br>internal/verify/TestScanAndVerify | verified |
| URS-10 | internal/connectors/TestEUFMDRejectionFixResubmit<br>internal/connectors/TestEUFMDContract<br>internal/connectors/TestSARSDContract | verified |
| URS-11 | internal/food/TestFoodTraceabilityRecallAndEUDR | verified |
| URS-12 | internal/act/TestAgentsApprovalsAndControlTower | verified |
| URS-13 | internal/dscsa/TestDSCSAManufacturerToWholesaler | verified |
| URS-14 | internal/saas/TestSaaSLifecycle | verified |
| URS-15 | internal/epcis/TestBench100k | verified |

## Executed tests

| Test | Result | Duration (s) |
|---|---|---|
| internal/act/TestAgentsApprovalsAndControlTower | pass | 3.20 |
| internal/act/TestWorkflowsFormsAndAssistant | pass | 1.30 |
| internal/app/TestSeedDemo | pass | 10.38 |
| internal/connectors/TestEUFMDContract | pass | 0.21 |
| internal/connectors/TestEUFMDRejectionFixResubmit | pass | 7.50 |
| internal/connectors/TestSARSDContract | pass | 0.00 |
| internal/dscsa/TestDSCSAManufacturerToWholesaler | pass | 1.59 |
| internal/ecosystem/TestMarketplaceSandboxColdChainUDI | pass | 2.05 |
| internal/epcis/TestBench100k | pass | 10.33 |
| internal/epcis/TestCaptureRejections | pass | 1.21 |
| internal/epcis/TestCaptureTraceLifecycle | pass | 1.16 |
| internal/epcis/TestProductCSVAndPackaging | pass | 0.84 |
| internal/epcis/TestXMLCaptureAndSubscription | pass | 0.95 |
| internal/food/TestFoodTraceabilityRecallAndEUDR | pass | 2.83 |
| internal/link/TestFederationTwoNodes | pass | 4.32 |
| internal/migration/TestMigrationAndShadow | pass | 1.44 |
| internal/platform/TestAuditImmutable | pass | 1.15 |
| internal/platform/TestPlatformFlow | pass | 1.36 |
| internal/platform/TestTenantIsolation | pass | 1.93 |
| internal/saas/TestSaaSLifecycle | pass | 2.20 |
| internal/serialization/TestDomainMinimum | pass | 0.00 |
| internal/serialization/TestLowWatermarkNoOverflow | pass | 0.00 |
| internal/serialization/TestRandomSerialsUniqueAndReversible | pass | 0.80 |
| internal/serialization/TestSequentialAndPrefix | pass | 0.00 |
| internal/verify/TestPlayground | pass | 1.01 |
| internal/verify/TestScanAndVerify | pass | 1.43 |
| pkg/appsdk/TestClientErrors | pass | 0.00 |
| pkg/appsdk/TestWebhook | pass | 0.00 |
| pkg/edi/TestEDIFACT | pass | 0.01 |
| pkg/edi/TestIDocIsXML | pass | 0.00 |
| pkg/edi/TestX12AllSets | pass | 0.00 |
| pkg/edi/TestX12OrderRoundTrip | pass | 0.00 |
| pkg/epcis/TestCBVForms | pass | 0.00 |
| pkg/epcis/TestEPCIS12XML | pass | 0.00 |
| pkg/epcis/TestExtensionsPreserved | pass | 0.00 |
| pkg/epcis/TestGS1Examples | pass | 0.00 |
| pkg/epcis/TestValidationErrors | pass | 0.00 |
| pkg/gs1/TestCheckDigits | pass | 0.00 |
| pkg/gs1/TestElementStringRoundTrip | pass | 0.00 |
| pkg/gs1/TestExpiryDate | pass | 0.00 |
| pkg/gs1/TestSGTINAndDigitalLink | pass | 0.00 |

## Approval

| Role | Name | Signature | Date |
|---|---|---|---|
| Executed by | | | |
| Reviewed by (QA) | | | |
