# labeler

`labeler` is a public GitHub app inspired by @cdr-bot that automatically 
labels issues and pull requests based on how recent issues and pull requests were
labeled.

It is free and available for all open source projects. If it gains traction,
we may open it up to private repositories as well, perhaps for a fee.

# Business Goals

The primary goal of `coder-labeler` is promotion of the Coder brand. Hopefully
we can attract major open source repositories to use it, and then their users
will see "@coder-labeler labeled this issue a X" across many trackers.

# Product Goals

* Enforce consistent use of labels in issue tracker so searching by labels is
  more useful
* Reduce toil of manual labelling in common cases

# Architecture

Initially, the labeler will use no persistent storage and will be essentially
stateless. My hope is that such an architecture will reduce maintenance burden
in the case that a few open source repos pick it up but we don't want to allocate
resources to it.

## Completion vs. Embedding

The labeler uses a GPT-4 completion with the past 100 opened issues instead of
a more complex vector DB / embedding system. This is because of the proven
accuracy of cdr-bot and the fact that the completion approach lets us remove
the need for a DB.

On the other side, completions are an order of magnitude more expensive, so
costs may approach ~10c per opened issue. If the project gets enough traction
to where that becomes an issue, we can change the model.
