import XCTest
@testable import Portico

final class HelperProtocolTests: XCTestCase {
    func testDecodesVersionOneHandshakeResponse() throws {
        let fixture = Data(#"{"version":1,"requestId":"request-1","result":{"protocolVersion":1}}"#.utf8)

        let response = try JSONDecoder().decode(HelperResponse<HandshakeResult>.self, from: fixture)

        XCTAssertEqual(response.version, 1)
        XCTAssertEqual(response.requestId, "request-1")
        XCTAssertEqual(response.result?.protocolVersion, 1)
        XCTAssertNil(response.error)
    }

    func testDecodesStructuredProtocolError() throws {
        let fixture = Data(#"{"version":1,"requestId":"request-2","error":{"code":"unknownCommand","message":"unsupported command"}}"#.utf8)

        let response = try JSONDecoder().decode(HelperResponse<HandshakeResult>.self, from: fixture)

        XCTAssertEqual(response.version, 1)
        XCTAssertEqual(response.requestId, "request-2")
        XCTAssertNil(response.result)
        XCTAssertEqual(response.error, HelperProtocolError(code: "unknownCommand", message: "unsupported command"))
    }
}
