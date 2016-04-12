load("@io_bazel_rules_go//go:def.bzl", "go_prefix", "go_library")

go_prefix("github.com/anupcshan/bazel-build-worker")

go_library(
    name = "go_default_library",
    srcs = glob(["*.go"]),
    deps = [
        "//remote:go_default_library",
    ],
)
