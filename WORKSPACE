git_repository(
    name = "io_bazel_rules_go",
    remote = "https://github.com/bazelbuild/rules_go.git",
    commit = "373feb67001252371054c3388291661352c4eb90",  # Tag 0.0.1 doesn't set go_prefix correctly
)

load("@io_bazel_rules_go//go:def.bzl", "go_repositories")

go_repositories()
