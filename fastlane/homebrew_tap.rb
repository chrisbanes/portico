require "fileutils"

HOMEBREW_TAP = "chrisbanes/tap"
PORTICO_ROOT = File.expand_path("..", __dir__)

def update_homebrew_tap(version:, checksums:, tap_token:, app_directory:, &publish_release)
  UI.user_error!("Homebrew tap update requires a release publication block") if publish_release.nil?

  arm64_checksum = checksums.fetch("arm64")
  x86_64_checksum = checksums.fetch("x86_64")

  installed = false
  state_root = File.expand_path("~/Library/Application Support/Portico")
  sh("brew", "tap", HOMEBREW_TAP)
  tap_directory = sh("brew", "--repository", HOMEBREW_TAP).strip
  cask_path = File.join(tap_directory, "Casks/portico.rb")
  FileUtils.mkdir_p(File.dirname(cask_path))
  File.write(cask_path, <<~CASK)
    cask "portico" do
      arch arm: "arm64", intel: "x86_64"

      version "#{version}"
      sha256 arm:   "#{arm64_checksum}",
             intel: "#{x86_64_checksum}"

      url "https://github.com/chrisbanes/portico/releases/download/v\#{version}/Portico-\#{version}-\#{arch}.dmg"
      name "Portico"
      desc "Make a web service on your Mac reachable on your tailnet"
      homepage "https://github.com/chrisbanes/portico"

      depends_on macos: :sonoma

      app "Portico.app"
    end
  CASK

  sh("brew", "audit", "--cask", "--strict", "#{HOMEBREW_TAP}/portico")
  publish_release.call

  3.times do |attempt|
    installed = sh("brew", "install", "--cask", "--appdir=#{app_directory}", "#{HOMEBREW_TAP}/portico") do |status, _result, _command|
      status.success?
    end
    break if installed

    sh("brew", "uninstall", "--cask", "--force", "#{HOMEBREW_TAP}/portico") { |_status, _result, _command| nil }
    sleep(15) if attempt < 2
  end
  UI.user_error!("Homebrew could not install the published cask after three attempts") unless installed

  FileUtils.rm_rf(state_root)
  FileUtils.mkdir_p(state_root)
  File.write(
    File.join(state_root, "installation-v4.json"),
    '{"version":4,"portals":[],"alerts":[],"operationalLogging":"disabled","launchAtLoginOffer":"notOffered"}'
  )
  FastlaneCore::Helper.with_env_values("PORTICO_APP_PATH" => File.join(app_directory, "Portico.app")) do
    sh(File.join(PORTICO_ROOT, "Scripts/smoke-test-local-app.sh"))
  end

  sh("git", "-C", tap_directory, "add", "--", "Casks/portico.rb")
  sh("git", "-C", tap_directory, "diff", "--cached", "--check")
  changed_paths = sh("git", "-C", tap_directory, "diff", "--cached", "--name-only").strip
  return if changed_paths.empty?

  UI.user_error!("Tap update must change only Casks/portico.rb") unless changed_paths == "Casks/portico.rb"
  sh("git", "-C", tap_directory, "config", "user.name", "github-actions[bot]")
  sh("git", "-C", tap_directory, "config", "user.email", "41898282+github-actions[bot]@users.noreply.github.com")
  sh("git", "-C", tap_directory, "commit", "-m", "Update portico to #{version}")

  FastlaneCore::Helper.with_env_values("GH_TOKEN" => tap_token) do
    sh("gh", "auth", "setup-git")
    sh("git", "-C", tap_directory, "push", "origin", "HEAD:main")
  end
ensure
  FileUtils.rm_rf(state_root)
  if installed
    sh("brew", "uninstall", "--cask", "--force", "#{HOMEBREW_TAP}/portico") { |_status, _result, _command| nil }
  end
  sh("brew", "untap", HOMEBREW_TAP) { |_status, _result, _command| nil }
end
