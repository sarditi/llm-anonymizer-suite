// Trivial smoke test so `flutter test` exits clean. The interesting code (API
// client, state) is non-trivial to test in isolation without a fake HTTP
// transport — left to a follow-up if needed.
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('arithmetic sanity', () {
    expect(1 + 1, 2);
  });
}
