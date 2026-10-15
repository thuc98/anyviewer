const messages = [
  [/Wrong Password|Sai mật khẩu/i, 'Sai mật khẩu. Vui lòng kiểm tra mật khẩu của đối tác.'],
  [/không online|ID không tồn tại/i, 'Đối tác không trực tuyến hoặc ID không tồn tại.'],
  [/session expired/i, 'Phiên làm việc đã hết hạn. Vui lòng tải lại trang.'],
  [/Partner ID and password are required/i, 'Vui lòng nhập ID và mật khẩu của đối tác.'],
  [/password must be a string/i, 'Mật khẩu không hợp lệ.'],
  [/wait for the connection attempt/i, 'Vui lòng chờ kết nối hoàn tất trước khi lưu cài đặt.'],
  [/session is not connected|session is closed/i, 'Chưa kết nối với đối tác.'],
  [/viewer is shutting down/i, 'Ứng dụng đang đóng.'],
  [/input queue is full/i, 'Đang xử lý quá nhiều thao tác. Vui lòng thử lại sau.'],
  [/stream bitrate must/i, 'Băng thông mục tiêu phải từ 100 đến 50.000 kbps.'],
  [/VP8 requires WebRTC/i, 'Vui lòng chọn WebRTC để sử dụng video VP8.'],
  [/VP8 video requires FFmpeg/i, 'Máy chủ cần cài FFmpeg với bộ mã hóa VP8 để truyền video.'],
  [/stream quality must/i, 'Chất lượng JPEG phải từ 1 đến 100.'],
  [/stream FPS must/i, 'Tốc độ khung hình phải từ 1 đến 60 khung/giây.'],
  [/unsupported stream transport|unsupported stream encoding/i, 'Cơ chế truyền hoặc định dạng hình ảnh không được hỗ trợ.'],
  [/registration host and TLS server name|SOCKS5 host must/i, 'Vui lòng nhập tên miền hoặc địa chỉ IP, không kèm giao thức hay cổng.'],
  [/SOCKS5 port must/i, 'Cổng SOCKS5 phải từ 1 đến 65.535.'],
  [/SOCKS5 credentials must/i, 'Thông tin đăng nhập SOCKS5 tối đa 255 byte; cần tên đăng nhập khi có mật khẩu.'],
  [/machine name must/i, 'Tên máy phải từ 1 đến 128 ký tự, không chứa dấu hai chấm hoặc ký tự điều khiển.'],
  [/unsupported UI theme|unsupported refresh interval|unsupported quality preset|invalid settings/i, 'Cài đặt không hợp lệ. Vui lòng kiểm tra lại các giá trị.'],
  [/file exceeds 50 MB|incoming file exceeds 50 MB/i, 'Tệp vượt quá giới hạn 50 MB.'],
  [/file is required/i, 'Vui lòng chọn tệp để gửi.'],
  [/file offer is no longer available/i, 'Yêu cầu gửi tệp không còn hiệu lực.'],
  [/too many incoming files/i, 'Có quá nhiều tệp đã nhận. Vui lòng kết nối lại để dọn danh sách.'],
  [/no active incoming file|no active.*file/i, 'Không có tệp đang được truyền.'],
  [/clipboard synchronization is disabled/i, 'Đồng bộ bộ nhớ tạm đang tắt.'],
  [/clipboard.*64 KB|clipboard exceeds/i, 'Văn bản trong bộ nhớ tạm vượt quá dung lượng cho phép.'],
  [/clipboard/i, 'Không thể đồng bộ bộ nhớ tạm. Vui lòng thử lại.'],
  [/timeout|timed out/i, 'Kết nối hoặc xử lý mất quá nhiều thời gian. Vui lòng thử lại.'],
  [/connection closed|data channel closed|^closed$|^disconnected$/i, 'Kết nối đã bị ngắt.'],
  [/browser does not support WebRTC/i, 'Trình duyệt chưa hỗ trợ WebRTC.'],
  [/connection error|Failed to fetch|fetch failed|NetworkError|^failed$|connection refused|no such host/i, 'Không thể kết nối tới máy chủ. Vui lòng kiểm tra mạng.'],
  [/invalid frame|Screen patch needs a full frame/i, 'Dữ liệu hình ảnh không hợp lệ. Đang kết nối lại.'],
  [/WebRTC|VP8 encod/i, 'Không thể khởi tạo hoặc xử lý luồng hình ảnh WebRTC.'],
  [/screen|JPEG|Zstandard|codec|BitBlt/i, 'Không thể giải mã hình ảnh từ đối tác. Vui lòng thử kết nối lại.'],
  [/tls|certificate/i, 'Không thể xác thực kết nối bảo mật tới máy chủ.'],
  [/file|multipart|transfer/i, 'Không thể xử lý tệp. Vui lòng thử lại.'],
  [/invalid|unsupported|missing field|payload too large/i, 'Dữ liệu yêu cầu không hợp lệ hoặc không được hỗ trợ.'],
];
export function errorMessage(error) {
  const message = typeof error === 'string' ? error : error?.message || '';
  if (error?.name === 'NotAllowedError') return 'Trình duyệt chưa cho phép thao tác này. Vui lòng kiểm tra quyền truy cập.';
  for (const [pattern, translated] of messages) if (pattern.test(message)) return translated;
  if (/[À-ỹĐđ]/u.test(message)) return message.replace('Status 99', 'mã 99');
  return 'Đã xảy ra lỗi. Vui lòng thử lại hoặc kiểm tra nhật ký máy chủ.';
}
